package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"wecomrelay/internal/model"
	"wecomrelay/internal/relay"
	"wecomrelay/internal/security"
	"wecomrelay/internal/wecom"
)

type replayFixture struct {
	engine  *gin.Engine
	db      *gorm.DB
	handler *DeadLetterHandler
	cipher  *security.Cipher
}

func newReplayFixture(t *testing.T) *replayFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", url.QueryEscape("dl-replay-"+t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.DeadLetter{}, &model.RelayTask{}, &model.Consumer{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		// 批量重推是并发执行的，内存 SQLite 用单连接避免写锁。
		sqlDB.SetMaxOpenConns(1)
	}

	cipher, err := security.NewCipher("dead-letter-replay-test-key")
	if err != nil {
		t.Fatalf("创建加密器失败: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewDeadLetterHandler(db, cipher, relay.NewSender(logger, false), logger)

	engine := gin.New()
	engine.POST("/dead-letters/:id/replay", handler.Replay)
	engine.POST("/dead-letters/replay-batch", handler.ReplayBatch)
	engine.GET("/dead-letters/:id", handler.Get)
	return &replayFixture{engine: engine, db: db, handler: handler, cipher: cipher}
}

func (f *replayFixture) encrypt(t *testing.T, value string) string {
	t.Helper()
	encrypted, err := f.cipher.Encrypt(value)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	return encrypted
}

func (f *replayFixture) createConsumer(t *testing.T, name, callbackURL, authType, authConfig string, enabled bool) *model.Consumer {
	t.Helper()
	consumer := &model.Consumer{
		Name:        name,
		CallbackURL: callbackURL,
		ForwardMode: model.ForwardModeJSON,
		AuthType:    authType,
		AuthConfig:  f.encrypt(t, authConfig),
		Enabled:     enabled,
		TimeoutMS:   3000,
		MaxRetry:    5,
	}
	if err := f.db.Create(consumer).Error; err != nil {
		t.Fatalf("创建消费端失败: %v", err)
	}
	return consumer
}

func (f *replayFixture) createDeadLetter(t *testing.T, eventID string, consumerID uint64, mode, callbackURL, status string) *model.DeadLetter {
	t.Helper()
	item := &model.DeadLetter{
		EventID:      eventID,
		ConsumerID:   consumerID,
		ConsumerName: "快照消费端",
		ForwardMode:  mode,
		CallbackURL:  callbackURL,
		RawXML:       `<xml><Event><![CDATA[change_contact]]></Event><UserID><![CDATA[zhangsan]]></UserID></xml>`,
		JSONPayload:  `{"event_id":"` + eventID + `","payload":{"user_id":"zhangsan"}}`,
		AuthType:     model.AuthTypeNone,
		AuthConfig:   f.encrypt(t, "{}"),
		TimeoutMS:    3000,
		Status:       status,
		RetryCount:   6,
	}
	if mode == model.ForwardModeWecomProxy {
		item.RawEncryptedBody = "<xml><Encrypt>encrypted-body</Encrypt></xml>"
		item.InboundQuery = "msg_signature=sig&timestamp=1&nonce=2"
	}
	if err := f.db.Create(item).Error; err != nil {
		t.Fatalf("创建死信失败: %v", err)
	}
	return item
}

func (f *replayFixture) createTask(t *testing.T, eventID string, consumerID uint64, status string) *model.RelayTask {
	t.Helper()
	task := &model.RelayTask{
		EventID:     eventID,
		ConsumerID:  consumerID,
		ForwardMode: model.ForwardModeJSON,
		JSONPayload: `{"event_id":"` + eventID + `"}`,
		Status:      status,
		RetryCount:  3,
		MaxRetry:    5,
		LastError:   "old error",
	}
	if err := f.db.Create(task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	return task
}

func (f *replayFixture) replay(t *testing.T, id uint64, useSnapshot bool) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/dead-letters/%d/replay", id)
	if useSnapshot {
		path += "?use_snapshot=true"
	}
	req := httptest.NewRequest(http.MethodPost, path, nil)
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v, body=%s", err, rec.Body.String())
	}
	if body.Code != 0 {
		t.Fatalf("响应 code 非 0: %d, body=%s", body.Code, rec.Body.String())
	}
	return body.Data
}

func randomReplayAESKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("生成 AESKey 失败: %v", err)
	}
	return base64.StdEncoding.EncodeToString(key)[:43]
}

func TestReplayRejectsInvalidUseSnapshot(t *testing.T) {
	f := newReplayFixture(t)
	dead := f.createDeadLetter(t, "evt_replay_invalid_flag", 1, model.ForwardModeJSON, "http://127.0.0.1:1/never", model.DeadStatusPending)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/dead-letters/%d/replay?use_snapshot=maybe", dead.ID), nil)
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 use_snapshot 应返回 400，实际 %d %s", rec.Code, rec.Body.String())
	}
}

func TestReplayUsesLatestConsumerConfigAndSyncsTask(t *testing.T) {
	f := newReplayFixture(t)

	var snapshotHits, latestHits int
	var latestAuth string
	snapshotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshotHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer snapshotServer.Close()
	latestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		latestHits++
		latestAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer latestServer.Close()

	consumer := f.createConsumer(t, "最新消费端", latestServer.URL, model.AuthTypeBearer, `{"token":"new-token"}`, true)
	dead := f.createDeadLetter(t, "evt_replay_latest", consumer.ID, model.ForwardModeJSON, snapshotServer.URL, model.DeadStatusPending)
	task := f.createTask(t, dead.EventID, consumer.ID, model.TaskStatusDead)

	rec := f.replay(t, dead.ID, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("重推应成功: %d %s", rec.Code, rec.Body.String())
	}
	data := decodeResponse(t, rec)
	if data["config_source"] != "latest" || data["task_synced"] != true {
		t.Fatalf("重推响应错误: %+v", data)
	}
	if latestHits != 1 || snapshotHits != 0 {
		t.Fatalf("应只请求最新消费端，latest=%d snapshot=%d", latestHits, snapshotHits)
	}
	if latestAuth != "Bearer new-token" {
		t.Fatalf("应使用最新 bearer token，实际 %q", latestAuth)
	}

	var updatedDead model.DeadLetter
	if err := f.db.First(&updatedDead, dead.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedDead.Status != model.DeadStatusResolved || updatedDead.LastError != "" {
		t.Fatalf("死信应收尾为 resolved: %+v", updatedDead)
	}

	var updatedTask model.RelayTask
	if err := f.db.First(&updatedTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedTask.Status != model.TaskStatusSuccess || updatedTask.LastHTTPStatus != http.StatusOK ||
		updatedTask.LastError != "" || updatedTask.NextRetryAt != nil || updatedTask.RetryCount != 3 {
		t.Fatalf("推送记录回写错误: %+v", updatedTask)
	}

	var audits int64
	f.db.Model(&model.AuditLog{}).Where("action = ? AND result = ?", "dead_letter.replay", "success").Count(&audits)
	if audits != 1 {
		t.Fatalf("应写入成功审计，实际 %d", audits)
	}
}

func TestReplaySnapshotModeBypassesDisabledConsumer(t *testing.T) {
	f := newReplayFixture(t)
	var snapshotHits, disabledHits int
	snapshotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshotHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer snapshotServer.Close()
	disabledServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disabledHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer disabledServer.Close()

	consumer := f.createConsumer(t, "禁用消费端", disabledServer.URL, model.AuthTypeNone, "{}", false)
	dead := f.createDeadLetter(t, "evt_replay_snapshot", consumer.ID, model.ForwardModeJSON, snapshotServer.URL, model.DeadStatusPending)

	rec := f.replay(t, dead.ID, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("快照重推应成功: %d %s", rec.Code, rec.Body.String())
	}
	data := decodeResponse(t, rec)
	if data["config_source"] != "snapshot" {
		t.Fatalf("应使用快照配置: %+v", data)
	}
	if snapshotHits != 1 || disabledHits != 0 {
		t.Fatalf("应只请求快照地址，snapshot=%d disabled=%d", snapshotHits, disabledHits)
	}
}

func TestReplayRejectsDisabledOrDeletedConsumerByDefault(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		f := newReplayFixture(t)
		var hits int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		consumer := f.createConsumer(t, "禁用消费端", server.URL, model.AuthTypeNone, "{}", false)
		dead := f.createDeadLetter(t, "evt_replay_disabled", consumer.ID, model.ForwardModeJSON, server.URL, model.DeadStatusPending)
		task := f.createTask(t, dead.EventID, consumer.ID, model.TaskStatusDead)

		rec := f.replay(t, dead.ID, false)
		if rec.Code != http.StatusConflict {
			t.Fatalf("禁用消费端默认重推应返回 409: %d %s", rec.Code, rec.Body.String())
		}
		if hits != 0 {
			t.Fatalf("不应发起 HTTP 请求，实际 %d", hits)
		}
		var updatedDead model.DeadLetter
		f.db.First(&updatedDead, dead.ID)
		if updatedDead.Status != model.DeadStatusPending {
			t.Fatalf("预检失败不应改变死信状态: %s", updatedDead.Status)
		}
		var updatedTask model.RelayTask
		f.db.First(&updatedTask, task.ID)
		if updatedTask.Status != model.TaskStatusDead {
			t.Fatalf("预检失败不应改变任务状态: %s", updatedTask.Status)
		}
	})

	t.Run("soft deleted", func(t *testing.T) {
		f := newReplayFixture(t)
		consumer := f.createConsumer(t, "已删除消费端", "http://127.0.0.1:1/never", model.AuthTypeNone, "{}", true)
		dead := f.createDeadLetter(t, "evt_replay_deleted", consumer.ID, model.ForwardModeJSON, "http://127.0.0.1:1/never", model.DeadStatusPending)
		if err := f.db.Delete(consumer).Error; err != nil {
			t.Fatal(err)
		}

		rec := f.replay(t, dead.ID, false)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "已删除") {
			t.Fatalf("软删除消费端默认重推应返回 409 且提示已删除: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("discarded stays discarded", func(t *testing.T) {
		f := newReplayFixture(t)
		consumer := f.createConsumer(t, "禁用消费端", "http://127.0.0.1:1/never", model.AuthTypeNone, "{}", false)
		dead := f.createDeadLetter(t, "evt_replay_discarded", consumer.ID, model.ForwardModeJSON, "http://127.0.0.1:1/never", model.DeadStatusDiscarded)

		rec := f.replay(t, dead.ID, false)
		if rec.Code != http.StatusConflict {
			t.Fatalf("应返回 409: %d", rec.Code)
		}
		var updated model.DeadLetter
		f.db.First(&updated, dead.ID)
		if updated.Status != model.DeadStatusDiscarded {
			t.Fatalf("预检失败不应改变 discarded 状态: %s", updated.Status)
		}
	})
}

func TestReplayFailureRevertsDeadLetterToPending(t *testing.T) {
	f := newReplayFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "consumer down", http.StatusInternalServerError)
	}))
	defer server.Close()

	consumer := f.createConsumer(t, "失败消费端", server.URL, model.AuthTypeNone, "{}", true)
	dead := f.createDeadLetter(t, "evt_replay_failure", consumer.ID, model.ForwardModeJSON, server.URL, model.DeadStatusPending)
	task := f.createTask(t, dead.EventID, consumer.ID, model.TaskStatusDead)

	rec := f.replay(t, dead.ID, false)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("投递失败应返回 502: %d %s", rec.Code, rec.Body.String())
	}
	var updatedDead model.DeadLetter
	f.db.First(&updatedDead, dead.ID)
	if updatedDead.Status != model.DeadStatusPending || updatedDead.LastError == "" {
		t.Fatalf("失败后死信应回退 pending 并记录错误: %+v", updatedDead)
	}
	var updatedTask model.RelayTask
	f.db.First(&updatedTask, task.ID)
	if updatedTask.Status != model.TaskStatusDead {
		t.Fatalf("重推失败不应修改任务状态: %s", updatedTask.Status)
	}
}

func TestReplaySkipsTaskWritebackWhenTaskMissingOrNotDead(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		f := newReplayFixture(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		consumer := f.createConsumer(t, "消费端", server.URL, model.AuthTypeNone, "{}", true)
		dead := f.createDeadLetter(t, "evt_replay_missing_task", consumer.ID, model.ForwardModeJSON, server.URL, model.DeadStatusPending)

		rec := f.replay(t, dead.ID, false)
		if rec.Code != http.StatusOK {
			t.Fatalf("无任务时重推仍应成功: %d %s", rec.Code, rec.Body.String())
		}
		data := decodeResponse(t, rec)
		if data["task_synced"] != false {
			t.Fatalf("无任务时 task_synced 应为 false: %+v", data)
		}
	})

	t.Run("not dead", func(t *testing.T) {
		f := newReplayFixture(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		consumer := f.createConsumer(t, "消费端", server.URL, model.AuthTypeNone, "{}", true)
		dead := f.createDeadLetter(t, "evt_replay_not_dead", consumer.ID, model.ForwardModeJSON, server.URL, model.DeadStatusPending)
		task := f.createTask(t, dead.EventID, consumer.ID, model.TaskStatusSuccess)

		rec := f.replay(t, dead.ID, false)
		if rec.Code != http.StatusOK {
			t.Fatalf("任务非 dead 时重推仍应成功: %d %s", rec.Code, rec.Body.String())
		}
		var updated model.RelayTask
		f.db.First(&updated, task.ID)
		if updated.Status != model.TaskStatusSuccess || updated.LastError != "old error" {
			t.Fatalf("非 dead 任务不应被覆盖: %+v", updated)
		}
	})
}

func TestReplayWecomXMLUsesLatestKeysAndAgentID(t *testing.T) {
	f := newReplayFixture(t)
	corpID := "ww-replay-corp"
	newToken := "new-wecom-token"
	newAESKey := randomReplayAESKey(t)
	crypt, err := wecom.NewCrypto(newToken, newAESKey, corpID)
	if err != nil {
		t.Fatalf("创建测试 Crypto 失败: %v", err)
	}

	var gotPlain, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		query := r.URL.Query()
		plain, err := crypt.DecryptMsg(query.Get("msg_signature"), query.Get("timestamp"), query.Get("nonce"), body)
		if err != nil {
			http.Error(w, "decrypt failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		gotPlain = plain
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	consumer := &model.Consumer{
		Name:                "最新企微消费端",
		CallbackURL:         server.URL,
		ForwardMode:         model.ForwardModeJSON, // 故意与死信快照不同，重推应仍按死信 ForwardMode
		AuthType:            model.AuthTypeNone,
		AuthConfig:          f.encrypt(t, "{}"),
		WecomToken:          f.encrypt(t, newToken),
		WecomEncodingAESKey: f.encrypt(t, newAESKey),
		WecomCorpID:         corpID,
		WecomAgentID:        "new-agent",
		Enabled:             true,
		TimeoutMS:           3000,
		MaxRetry:            5,
	}
	if err := f.db.Create(consumer).Error; err != nil {
		t.Fatal(err)
	}

	rawXML := `<xml><Event><![CDATA[change_contact]]></Event><UserID><![CDATA[zhangsan]]></UserID></xml>`
	dead := f.createDeadLetter(t, "evt_replay_wecom_xml", consumer.ID, model.ForwardModeWecomXML, "http://127.0.0.1:1/old", model.DeadStatusPending)
	if err := f.db.Model(dead).Updates(map[string]any{
		"raw_xml":                rawXML,
		"agent_id":               "old-agent",
		"wecom_corp_id":          "ww-old-corp",
		"wecom_token_snapshot":   f.encrypt(t, "old-token"),
		"wecom_aes_key_snapshot": f.encrypt(t, randomReplayAESKey(t)),
	}).Error; err != nil {
		t.Fatal(err)
	}

	rec := f.replay(t, dead.ID, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("wecom_xml 重推应成功: %d %s", rec.Code, rec.Body.String())
	}
	if gotPlain != rawXML {
		t.Fatalf("消费端应能用最新密钥解密，实际明文 %q", gotPlain)
	}
	if !strings.Contains(gotBody, "new-agent") || strings.Contains(gotBody, "old-agent") {
		t.Fatalf("外层 AgentID 应使用最新消费端配置: %s", gotBody)
	}
}

func TestReplayProxyUsesLatestURLAndSnapshotPayload(t *testing.T) {
	f := newReplayFixture(t)
	var latestBody, latestQuery string
	var snapshotHits int
	latestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		latestBody = string(body)
		latestQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer latestServer.Close()
	snapshotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshotHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer snapshotServer.Close()

	consumer := f.createConsumer(t, "最新消费端", latestServer.URL, model.AuthTypeNone, "{}", true)
	dead := f.createDeadLetter(t, "evt_replay_proxy", consumer.ID, model.ForwardModeWecomProxy, snapshotServer.URL, model.DeadStatusPending)

	rec := f.replay(t, dead.ID, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy 重推应成功: %d %s", rec.Code, rec.Body.String())
	}
	if snapshotHits != 0 {
		t.Fatalf("不应请求旧快照地址，实际 %d", snapshotHits)
	}
	if latestBody != dead.RawEncryptedBody {
		t.Fatalf("原始加密 Body 应原样转发: %q", latestBody)
	}
	if latestQuery != dead.InboundQuery {
		t.Fatalf("原始 Query 应原样转发: %q", latestQuery)
	}
}

func TestReplayBatchAuditResultPartial(t *testing.T) {
	f := newReplayFixture(t)
	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer successServer.Close()
	disabledServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer disabledServer.Close()

	okConsumer := f.createConsumer(t, "成功消费端", successServer.URL, model.AuthTypeNone, "{}", true)
	disabledConsumer := f.createConsumer(t, "禁用消费端", disabledServer.URL, model.AuthTypeNone, "{}", false)
	okDead := f.createDeadLetter(t, "evt_batch_ok", okConsumer.ID, model.ForwardModeJSON, successServer.URL, model.DeadStatusPending)
	failedDead := f.createDeadLetter(t, "evt_batch_failed", disabledConsumer.ID, model.ForwardModeJSON, disabledServer.URL, model.DeadStatusPending)

	body, _ := json.Marshal(map[string]any{
		"ids": []uint64{okDead.ID, failedDead.ID, 999999},
	})
	req := httptest.NewRequest(http.MethodPost, "/dead-letters/replay-batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("批量重推应返回 200: %d %s", rec.Code, rec.Body.String())
	}
	data := decodeResponse(t, rec)
	if data["result"] != "partial" || data["success"] != float64(1) || data["failed"] != float64(2) {
		t.Fatalf("批量统计错误: %+v", data)
	}
	var audit model.AuditLog
	if err := f.db.Where("action = ?", "dead_letter.replay_batch").First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.Result != "partial" {
		t.Fatalf("批量审计结果应为 partial，实际 %q", audit.Result)
	}
}

func TestGetDeadLetterIncludesConsumerState(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		f := newReplayFixture(t)
		consumer := f.createConsumer(t, "正常消费端", "http://example.test/callback", model.AuthTypeNone, "{}", true)
		dead := f.createDeadLetter(t, "evt_state_normal", consumer.ID, model.ForwardModeJSON, consumer.CallbackURL, model.DeadStatusPending)

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/dead-letters/%d", dead.ID), nil)
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("详情查询失败: %d %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data struct {
				ConsumerExists     bool   `json:"consumer_exists"`
				ConsumerDeleted    bool   `json:"consumer_deleted"`
				ConsumerEnabled    bool   `json:"consumer_enabled"`
				CurrentCallbackURL string `json:"current_callback_url"`
				CurrentForwardMode string `json:"current_forward_mode"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析详情失败: %v", err)
		}
		if !resp.Data.ConsumerExists || resp.Data.ConsumerDeleted || !resp.Data.ConsumerEnabled ||
			resp.Data.CurrentCallbackURL != consumer.CallbackURL || resp.Data.CurrentForwardMode != model.ForwardModeJSON {
			t.Fatalf("当前消费端状态错误: %+v", resp.Data)
		}
	})

	t.Run("soft deleted", func(t *testing.T) {
		f := newReplayFixture(t)
		consumer := f.createConsumer(t, "已删除消费端", "http://example.test/callback", model.AuthTypeNone, "{}", true)
		dead := f.createDeadLetter(t, "evt_state_deleted", consumer.ID, model.ForwardModeJSON, consumer.CallbackURL, model.DeadStatusPending)
		if err := f.db.Delete(consumer).Error; err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/dead-letters/%d", dead.ID), nil)
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, req)
		var resp struct {
			Data struct {
				ConsumerExists  bool `json:"consumer_exists"`
				ConsumerDeleted bool `json:"consumer_deleted"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析详情失败: %v", err)
		}
		if !resp.Data.ConsumerExists || !resp.Data.ConsumerDeleted {
			t.Fatalf("软删除消费端状态错误: %+v", resp.Data)
		}
	})

	t.Run("missing", func(t *testing.T) {
		f := newReplayFixture(t)
		dead := f.createDeadLetter(t, "evt_state_missing", 999999, model.ForwardModeJSON, "http://example.test/callback", model.DeadStatusPending)
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/dead-letters/%d", dead.ID), nil)
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, req)
		var resp struct {
			Data struct {
				ConsumerExists bool `json:"consumer_exists"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析详情失败: %v", err)
		}
		if resp.Data.ConsumerExists {
			t.Fatalf("不存在的消费端不应标记为存在: %+v", resp.Data)
		}
	})
}
