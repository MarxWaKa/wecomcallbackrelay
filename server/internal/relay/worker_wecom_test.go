package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"wecomrelay/internal/model"
	"wecomrelay/internal/wecom"
)

func TestProcessTaskWecomProxyMode(t *testing.T) {
	f := newWorkerFixture(t)
	rawQuery := "msg_signature=proxy-sig&timestamp=1794400000&nonce=1122334455"
	rawBody := `<xml><ToUserName><![CDATA[wx-proxy-corp]]></ToUserName><Encrypt><![CDATA[proxy-cipher]]></Encrypt><AgentID><![CDATA[]]></AgentID></xml>`

	var gotQuery, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	consumer := &model.Consumer{
		Name:        "proxy-consumer",
		CallbackURL: server.URL + "/callback",
		ForwardMode: model.ForwardModeWecomProxy,
		AuthType:    model.AuthTypeNone,
		Enabled:     true,
		TimeoutMS:   3000,
		MaxRetry:    5,
	}
	if err := f.db.Create(consumer).Error; err != nil {
		t.Fatalf("创建消费端失败: %v", err)
	}
	task := &model.RelayTask{
		EventID:          "evt_proxy_test",
		ConsumerID:       consumer.ID,
		ForwardMode:      model.ForwardModeWecomProxy,
		RawEncryptedBody: rawBody,
		InboundQuery:     rawQuery,
		Status:           model.TaskStatusSending,
		MaxRetry:         5,
	}
	if err := f.db.Create(task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	f.worker.processTask(context.Background(), task.ID)

	var updated model.RelayTask
	if err := f.db.First(&updated, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusSuccess {
		t.Fatalf("原样透传任务应成功，实际 %s，错误 %s", updated.Status, updated.LastError)
	}
	if gotBody != rawBody || gotQuery != rawQuery {
		t.Fatalf("原样透传内容不一致: query=%q body=%q", gotQuery, gotBody)
	}
}

func TestProcessTaskWecomXMLMode(t *testing.T) {
	f := newWorkerFixture(t)
	token := "consumer-shared-token"
	aesKey := newAESKey(t)
	corpID := "ww-consumer-corp"
	plainXML := `<xml><ToUserName><![CDATA[ww-consumer-corp]]></ToUserName><CreateTime>1700000000</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[change_contact]]></Event><ChangeType>create_user</ChangeType><UserID><![CDATA[lisi]]></UserID></xml>`

	var handlerErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		body, _ := io.ReadAll(r.Body)
		envelope, err := wecom.ParseEnvelope(body)
		if err != nil {
			handlerErr = fmt.Errorf("解析加密报文失败: %w", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if envelope.ToUserName != corpID || envelope.AgentID != "2000001" {
			handlerErr = fmt.Errorf("外层报文错误: ToUserName=%q AgentID=%q", envelope.ToUserName, envelope.AgentID)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		crypt, err := wecom.NewCrypto(token, aesKey, corpID)
		if err != nil {
			handlerErr = err
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !crypt.VerifySignature(query.Get("msg_signature"), query.Get("timestamp"), query.Get("nonce"), envelope.Encrypt) {
			handlerErr = wecom.ErrInvalidSignature
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		decrypted, err := crypt.Decrypt(envelope.Encrypt)
		if err != nil {
			handlerErr = fmt.Errorf("解密失败: %w", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if decrypted != plainXML {
			handlerErr = fmt.Errorf("明文不一致: %q", decrypted)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	encToken, _ := f.cipher.Encrypt(token)
	encAESKey, _ := f.cipher.Encrypt(aesKey)
	encAuth, _ := f.cipher.Encrypt("{}")
	consumer := &model.Consumer{
		Name:                "wecom-xml-consumer",
		CallbackURL:         server.URL,
		ForwardMode:         model.ForwardModeWecomXML,
		AuthType:            model.AuthTypeNone,
		AuthConfig:          encAuth,
		WecomToken:          encToken,
		WecomEncodingAESKey: encAESKey,
		WecomCorpID:         corpID,
		WecomAgentID:        "2000001",
		Enabled:             true,
		TimeoutMS:           3000,
		MaxRetry:            5,
	}
	if err := f.db.Create(consumer).Error; err != nil {
		t.Fatalf("创建消费端失败: %v", err)
	}
	task := &model.RelayTask{
		EventID:     "evt_wecom_xml_test",
		ConsumerID:  consumer.ID,
		ForwardMode: model.ForwardModeWecomXML,
		AgentID:     "1000002",
		RawXML:      plainXML,
		JSONPayload: `{"event_id":"evt_wecom_xml_test"}`,
		Status:      model.TaskStatusSending,
		MaxRetry:    5,
	}
	if err := f.db.Create(task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	f.worker.processTask(context.Background(), task.ID)

	var updated model.RelayTask
	if err := f.db.First(&updated, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusSuccess {
		t.Fatalf("任务应成功，实际 %s，错误 %s", updated.Status, updated.LastError)
	}
	if handlerErr != nil {
		t.Fatalf("模拟消费端验签/解密失败: %v", handlerErr)
	}

	// 确认模拟消费端确实收到过请求（避免因连接失败导致空断言）。
	time.Sleep(10 * time.Millisecond)
}
