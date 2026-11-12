package relay

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wecomrelay/internal/model"
	"wecomrelay/internal/wecom"
)

func testSender() *Sender {
	return NewSender(slog.New(slog.NewTextHandler(io.Discard, nil)), false)
}

func newAESKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)[:43]
}

func TestDeliverJSONWithEventID(t *testing.T) {
	var gotBody []byte
	var gotEventID, gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotEventID = r.Header.Get("X-Event-Id")
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	payloadJSON := `{"event_id":"evt_1","payload":{"user_id":"zhangsan"}}`
	status, _, err := testSender().Deliver(context.Background(), Target{
		CallbackURL: server.URL,
		ForwardMode: model.ForwardModeJSON,
		AuthType:    model.AuthTypeNone,
		TimeoutMS:   3000,
	}, Payload{EventID: "evt_1", JSONPayload: payloadJSON})
	if err != nil {
		t.Fatalf("投递失败: %v", err)
	}
	if status != 200 {
		t.Fatalf("状态码错误: %d", status)
	}
	if string(gotBody) != payloadJSON {
		t.Fatalf("请求体不一致: %s", gotBody)
	}
	if gotEventID != "evt_1" {
		t.Fatalf("X-Event-Id 错误: %q", gotEventID)
	}
	if gotContentType != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type 错误: %q", gotContentType)
	}
}

func TestDeliverJSONWithHMAC(t *testing.T) {
	var gotSig, gotTS string
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-Signature")
		gotTS = r.Header.Get("X-Timestamp")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	configJSON, _ := json.Marshal(map[string]string{"secret": "hmac-secret"})
	payloadJSON := `{"event_id":"evt_hmac"}`
	_, _, err := testSender().Deliver(context.Background(), Target{
		CallbackURL: server.URL,
		ForwardMode: model.ForwardModeJSON,
		AuthType:    model.AuthTypeHMAC,
		AuthConfig:  string(configJSON),
		TimeoutMS:   3000,
	}, Payload{EventID: "evt_hmac", JSONPayload: payloadJSON})
	if err != nil {
		t.Fatalf("投递失败: %v", err)
	}

	mac := hmac.New(sha256.New, []byte("hmac-secret"))
	mac.Write([]byte(gotTS + "." + string(body)))
	want := hex.EncodeToString(mac.Sum(nil))
	if gotSig != want {
		t.Fatalf("HMAC 签名错误: got %s want %s", gotSig, want)
	}
}

func TestDeliverJSONWithBearer(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	configJSON, _ := json.Marshal(map[string]string{"token": "abc123"})
	_, _, err := testSender().Deliver(context.Background(), Target{
		CallbackURL: server.URL,
		ForwardMode: model.ForwardModeJSON,
		AuthType:    model.AuthTypeBearer,
		AuthConfig:  string(configJSON),
		TimeoutMS:   3000,
	}, Payload{EventID: "evt_bearer", JSONPayload: `{}`})
	if err != nil {
		t.Fatalf("投递失败: %v", err)
	}
	if auth != "Bearer abc123" {
		t.Fatalf("Authorization 错误: %q", auth)
	}
}

func TestDeliverWecomXMLRoundTrip(t *testing.T) {
	token := "shared-token"
	aesKey := newAESKey(t)
	corpID := "ww-test-corp"
	plainXML := `<xml><ToUserName><![CDATA[ww-test-corp]]></ToUserName><Event><![CDATA[change_contact]]></Event><UserID><![CDATA[lisi]]></UserID></xml>`

	var verifyErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		body, _ := io.ReadAll(r.Body)
		encrypt, err := wecom.ParseEncryptedEnvelope(body)
		if err != nil {
			verifyErr = err
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		crypt, err := wecom.NewCrypto(token, aesKey, corpID)
		if err != nil {
			verifyErr = err
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !crypt.VerifySignature(query.Get("msg_signature"), query.Get("timestamp"), query.Get("nonce"), encrypt) {
			verifyErr = wecom.ErrInvalidSignature
			w.WriteHeader(http.StatusForbidden)
			return
		}
		decrypted, err := crypt.Decrypt(encrypt)
		if err != nil {
			verifyErr = err
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if decrypted != plainXML {
			verifyErr = io.ErrUnexpectedEOF
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	status, _, err := testSender().Deliver(context.Background(), Target{
		CallbackURL:         server.URL,
		ForwardMode:         model.ForwardModeWecomXML,
		WecomToken:          token,
		WecomEncodingAESKey: aesKey,
		WecomCorpID:         corpID,
		TimeoutMS:           3000,
	}, Payload{EventID: "evt_xml", RawXML: plainXML})
	if err != nil {
		t.Fatalf("投递失败: %v", err)
	}
	if status != 200 {
		t.Fatalf("状态码错误: %d", status)
	}
	if verifyErr != nil {
		t.Fatalf("消费端验签/解密失败: %v", verifyErr)
	}
}

func TestDeliverProxyPreservesRawRequest(t *testing.T) {
	var gotQuery, gotBody, gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotBodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(gotBodyBytes)
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rawQuery := "msg_signature=abc123&timestamp=1794400000&nonce=1234567890"
	rawBody := `<xml><ToUserName><![CDATA[ww-test-corp]]></ToUserName><Encrypt><![CDATA[raw-cipher]]></Encrypt><AgentID><![CDATA[]]></AgentID></xml>`
	status, _, err := testSender().DeliverProxy(context.Background(), Target{
		CallbackURL: server.URL + "/callback",
		ForwardMode: model.ForwardModeWecomProxy,
		TimeoutMS:   3000,
	}, rawBody, rawQuery)
	if err != nil {
		t.Fatalf("原样透传失败: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("状态码错误: %d", status)
	}
	if gotBody != rawBody {
		t.Fatalf("原始 Body 被修改:\n got %s\nwant %s", gotBody, rawBody)
	}
	if gotQuery != rawQuery {
		t.Fatalf("原始 Query 被修改:\n got %s\nwant %s", gotQuery, rawQuery)
	}
	if gotContentType != "text/xml" {
		t.Fatalf("Content-Type 错误: %q", gotContentType)
	}
}

func TestBuildWecomCallbackBodyIsWecomCompatible(t *testing.T) {
	body := buildWecomCallbackBody("ww-test-corp", "", "abc+/=")
	want := `<xml><ToUserName><![CDATA[ww-test-corp]]></ToUserName><Encrypt><![CDATA[abc+/=]]></Encrypt><AgentID><![CDATA[]]></AgentID></xml>`
	if body != want {
		t.Fatalf("企微回调报文格式错误:\n got %s\nwant %s", body, want)
	}
	encrypt, err := wecom.ParseEncryptedEnvelope([]byte(body))
	if err != nil {
		t.Fatalf("解析报文失败: %v", err)
	}
	if encrypt != "abc+/=" {
		t.Fatalf("Encrypt 内容错误: %q", encrypt)
	}

	// 透传 AgentID 时应与企微外层报文结构一致（顺序：ToUserName → Encrypt → AgentID）。
	withAgent := buildWecomCallbackBody("ww-test-corp", "1000002", "abc")
	wantWithAgent := `<xml><ToUserName><![CDATA[ww-test-corp]]></ToUserName><Encrypt><![CDATA[abc]]></Encrypt><AgentID><![CDATA[1000002]]></AgentID></xml>`
	if withAgent != wantWithAgent {
		t.Fatalf("带 AgentID 的报文格式错误:\n got %s\nwant %s", withAgent, wantWithAgent)
	}

	// CDATA 中出现 "]]>" 时应安全拆分。
	escaped := buildWecomCallbackBody("a]]>b", "1]]>2", "x")
	if !strings.Contains(escaped, "]]]]><![CDATA[>") {
		t.Fatalf("CDATA 转义失败: %s", escaped)
	}
}

func TestDeliverNon2xxReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "consumer down", http.StatusBadGateway)
	}))
	defer server.Close()

	status, _, err := testSender().Deliver(context.Background(), Target{
		CallbackURL: server.URL,
		ForwardMode: model.ForwardModeJSON,
		AuthType:    model.AuthTypeNone,
		TimeoutMS:   3000,
	}, Payload{EventID: "evt_err", JSONPayload: `{}`})
	if err == nil {
		t.Fatal("非 2xx 响应应返回错误")
	}
	if status != http.StatusBadGateway {
		t.Fatalf("状态码错误: %d", status)
	}
}
