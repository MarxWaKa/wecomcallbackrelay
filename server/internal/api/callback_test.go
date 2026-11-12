package api

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"wecomrelay/internal/model"
	"wecomrelay/internal/security"
	"wecomrelay/internal/wecom"
)

type callbackFixture struct {
	engine *gin.Engine
	db     *gorm.DB
	crypt  *wecom.Crypto
	token  string
	aesKey string
	corpID string
}

func newCallbackFixture(t *testing.T) *callbackFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", url.QueryEscape(t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.WecomConfig{}, &model.Consumer{}, &model.RelayTask{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}

	cipher, err := security.NewCipher("test-encrypt-key-for-integration")
	if err != nil {
		t.Fatalf("创建加密器失败: %v", err)
	}
	aesKey := randomAESKey(t)
	token := "callback-test-token"
	corpID := "ww-test-corp"

	encToken, _ := cipher.Encrypt(token)
	encAESKey, _ := cipher.Encrypt(aesKey)
	if err := db.Create(&model.WecomConfig{
		CorpID:         corpID,
		Secret:         "unused-secret",
		Token:          encToken,
		EncodingAESKey: encAESKey,
	}).Error; err != nil {
		t.Fatalf("创建企微参数失败: %v", err)
	}
	if err := db.Create(&model.Consumer{
		Name:        "测试消费端",
		CallbackURL: "http://127.0.0.1:1/not-used",
		ForwardMode: model.ForwardModeJSON,
		AuthType:    model.AuthTypeNone,
		Enabled:     true,
		TimeoutMS:   5000,
		MaxRetry:    5,
	}).Error; err != nil {
		t.Fatalf("创建消费端失败: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewCallbackHandler(db, cipher, logger, 300*time.Second)
	engine := gin.New()
	engine.GET("/wecom/callback", handler.Verify)
	engine.POST("/wecom/callback", handler.Receive)

	crypt, err := wecom.NewCrypto(token, aesKey, corpID)
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}
	return &callbackFixture{engine: engine, db: db, crypt: crypt, token: token, aesKey: aesKey, corpID: corpID}
}

func (f *callbackFixture) taskCount(t *testing.T) int64 {
	t.Helper()
	var count int64
	if err := f.db.Model(&model.RelayTask{}).Count(&count).Error; err != nil {
		t.Fatalf("统计任务失败: %v", err)
	}
	return count
}

func (f *callbackFixture) signedQuery(encrypt string) url.Values {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "testnonce"
	return url.Values{
		"msg_signature": {f.crypt.Signature(timestamp, nonce, encrypt)},
		"timestamp":     {timestamp},
		"nonce":         {nonce},
	}
}

func TestCallbackVerifyURL(t *testing.T) {
	f := newCallbackFixture(t)
	echoStr, err := f.crypt.Encrypt("hello-from-wecom")
	if err != nil {
		t.Fatalf("加密 echostr 失败: %v", err)
	}
	query := f.signedQuery(echoStr)
	query.Set("echostr", echoStr)

	req := httptest.NewRequest(http.MethodGet, "/wecom/callback?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET 验证状态码 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "hello-from-wecom" {
		t.Fatalf("GET 验证应原样返回明文，实际: %q", rec.Body.String())
	}
}

func TestCallbackReceiveCreatesTasksAndDeduplicates(t *testing.T) {
	f := newCallbackFixture(t)
	plain := `<xml><ToUserName><![CDATA[ww-test-corp]]></ToUserName><CreateTime>1699999999</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[change_contact]]></Event><ChangeType>create_user</ChangeType><UserID><![CDATA[zhangsan]]></UserID><Department><![CDATA[1,2]]></Department><MsgId>msg-1001</MsgId></xml>`

	send := func() *httptest.ResponseRecorder {
		encrypt, err := f.crypt.Encrypt(plain)
		if err != nil {
			t.Fatalf("加密事件失败: %v", err)
		}
		body := fmt.Sprintf("<xml><Encrypt><![CDATA[%s]]></Encrypt><ToUserName><![CDATA[ww-test-corp]]></ToUserName><AgentID><![CDATA[1000002]]></AgentID></xml>", encrypt)
		req := httptest.NewRequest(http.MethodPost, "/wecom/callback?"+f.signedQuery(encrypt).Encode(), strings.NewReader(body))
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, req)
		return rec
	}

	first := send()
	if first.Code != http.StatusOK || first.Body.String() != "success" {
		t.Fatalf("POST 回调失败: %d %q", first.Code, first.Body.String())
	}
	if count := f.taskCount(t); count != 1 {
		t.Fatalf("应创建 1 条投递任务，实际 %d", count)
	}

	var task model.RelayTask
	if err := f.db.First(&task).Error; err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.EventID != "evt_msg-1001" {
		t.Fatalf("event_id 错误: %q", task.EventID)
	}
	if task.ForwardMode != model.ForwardModeJSON || task.Status != model.TaskStatusPending {
		t.Fatalf("任务快照错误: %+v", task)
	}
	if task.AgentID != "1000002" {
		t.Fatalf("AgentID 透传失败: %q", task.AgentID)
	}
	if task.RawXML != plain {
		t.Fatal("raw_xml 快照与明文不一致")
	}
	if !strings.Contains(task.JSONPayload, `"user_id":"zhangsan"`) || !strings.Contains(task.JSONPayload, `"department":[1,2]`) {
		t.Fatalf("JSON 载荷错误: %s", task.JSONPayload)
	}

	// 企微重复投递同一 MsgId 时不应产生重复任务。
	second := send()
	if second.Code != http.StatusOK {
		t.Fatalf("重复回调应返回 200，实际 %d", second.Code)
	}
	if count := f.taskCount(t); count != 1 {
		t.Fatalf("重复事件应幂等，任务数实际 %d", count)
	}
}

func TestCallbackCreatesWecomProxyTaskSnapshot(t *testing.T) {
	f := newCallbackFixture(t)

	// 禁用默认 JSON 消费端，只保留原样透传消费端。
	var existing model.Consumer
	if err := f.db.First(&existing).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&existing).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	proxyConsumer := model.Consumer{
		Name:        "代理消费端",
		CallbackURL: "http://127.0.0.1:1/callback",
		ForwardMode: model.ForwardModeWecomProxy,
		AuthType:    model.AuthTypeNone,
		Enabled:     true,
		TimeoutMS:   5000,
		MaxRetry:    5,
	}
	if err := f.db.Create(&proxyConsumer).Error; err != nil {
		t.Fatal(err)
	}

	plain := `<xml><ToUserName><![CDATA[ww-test-corp]]></ToUserName><CreateTime>1699999999</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[change_contact]]></Event><ChangeType>create_user</ChangeType><UserID><![CDATA[zhangsan]]></UserID><Department><![CDATA[1]]></Department><MsgId>msg-proxy-1001</MsgId></xml>`
	encrypt, err := f.crypt.Encrypt(plain)
	if err != nil {
		t.Fatalf("加密事件失败: %v", err)
	}
	body := fmt.Sprintf("<xml><Encrypt><![CDATA[%s]]></Encrypt><ToUserName><![CDATA[ww-test-corp]]></ToUserName><AgentID><![CDATA[1000049]]></AgentID></xml>", encrypt)
	req := httptest.NewRequest(http.MethodPost, "/wecom/callback?"+f.signedQuery(encrypt).Encode(), strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "success" {
		t.Fatalf("POST 回调失败: %d %q", rec.Code, rec.Body.String())
	}

	var task model.RelayTask
	if err := f.db.Where("consumer_id = ?", proxyConsumer.ID).First(&task).Error; err != nil {
		t.Fatalf("应创建原样透传任务: %v", err)
	}
	if task.ForwardMode != model.ForwardModeWecomProxy {
		t.Fatalf("forward_mode 错误: %s", task.ForwardMode)
	}
	if task.RawEncryptedBody != body {
		t.Fatal("原样透传任务未保存原始加密 Body")
	}
	if task.InboundQuery == "" || task.InboundQuery != req.URL.RawQuery {
		t.Fatalf("原样透传任务未保存原始 Query: %q", task.InboundQuery)
	}
	if task.AgentID != "1000049" {
		t.Fatalf("AgentID 快照错误: %q", task.AgentID)
	}
}

func TestCallbackRejectsBadSignature(t *testing.T) {
	f := newCallbackFixture(t)
	plain := `<xml><Event><![CDATA[change_contact]]></Event><MsgId>msg-2001</MsgId></xml>`
	encrypt, _ := f.crypt.Encrypt(plain)
	body := fmt.Sprintf("<xml><Encrypt><![CDATA[%s]]></Encrypt></xml>", encrypt)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	query := url.Values{
		"msg_signature": {"deadbeef"},
		"timestamp":     {timestamp},
		"nonce":         {"nonce"},
	}

	req := httptest.NewRequest(http.MethodPost, "/wecom/callback?"+query.Encode(), strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("错误签名应返回 403，实际 %d", rec.Code)
	}
	if count := f.taskCount(t); count != 0 {
		t.Fatalf("验签失败不应入库，实际任务数 %d", count)
	}
}

func TestCallbackRejectsExpiredTimestamp(t *testing.T) {
	f := newCallbackFixture(t)
	encrypt, _ := f.crypt.Encrypt(`<xml><MsgId>msg-3001</MsgId></xml>`)
	body := fmt.Sprintf("<xml><Encrypt><![CDATA[%s]]></Encrypt></xml>", encrypt)

	timestamp := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	query := url.Values{
		"msg_signature": {f.crypt.Signature(timestamp, "nonce", encrypt)},
		"timestamp":     {timestamp},
		"nonce":         {"nonce"},
	}
	req := httptest.NewRequest(http.MethodPost, "/wecom/callback?"+query.Encode(), strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("过期时间戳应返回 403，实际 %d", rec.Code)
	}
	if count := f.taskCount(t); count != 0 {
		t.Fatalf("过期时间戳不应入库，实际任务数 %d", count)
	}
}

func TestCallbackMalformedPlaintextReturnsSuccessWithoutTask(t *testing.T) {
	f := newCallbackFixture(t)
	encrypt, _ := f.crypt.Encrypt(`<xml><broken>`)
	body := fmt.Sprintf("<xml><Encrypt><![CDATA[%s]]></Encrypt></xml>", encrypt)
	req := httptest.NewRequest(http.MethodPost, "/wecom/callback?"+f.signedQuery(encrypt).Encode(), strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "success" {
		t.Fatalf("明文解析失败应返回 200 success，实际 %d %q", rec.Code, rec.Body.String())
	}
	if count := f.taskCount(t); count != 0 {
		t.Fatalf("明文解析失败不应入库，实际任务数 %d", count)
	}
}

func randomAESKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	return base64.StdEncoding.EncodeToString(key)[:43]
}
