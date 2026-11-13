package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"wecomrelay/internal/model"
)

func newPushRecordFixture(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", url.QueryEscape("push-record-"+t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.RelayTask{}, &model.Consumer{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewPushRecordHandler(db, logger)

	engine := gin.New()
	engine.GET("/push-records", handler.List)
	engine.GET("/push-records/:id", handler.Get)
	engine.DELETE("/push-records/:id", handler.Delete)
	engine.POST("/push-records/batch-delete", handler.DeleteBatch)
	return engine, db
}

func createTestPushRecord(t *testing.T, db *gorm.DB, eventID, status string, consumerID uint64) *model.RelayTask {
	t.Helper()
	item := &model.RelayTask{
		EventID:          eventID,
		ConsumerID:       consumerID,
		ConsumerName:     "测试消费端",
		CallbackURL:      "http://example.test/callback",
		ForwardMode:      model.ForwardModeJSON,
		AgentID:          "1000002",
		RawXML:           "<xml><UserID>zhangsan</UserID></xml>",
		JSONPayload:      `{"event_id":"` + eventID + `","payload":{"user_id":"zhangsan"}}`,
		RawEncryptedBody: "encrypted-body",
		InboundQuery:     "msg_signature=sig&timestamp=1&nonce=2",
		Status:           status,
		RetryCount:       1,
		MaxRetry:         5,
		LastHTTPStatus:   http.StatusOK,
		LastLatencyMS:    12,
		LastError:        "",
	}
	if err := db.Create(item).Error; err != nil {
		t.Fatalf("创建推送记录失败: %v", err)
	}
	return item
}

func TestListPushRecordsFilters(t *testing.T) {
	engine, db := newPushRecordFixture(t)
	createTestPushRecord(t, db, "evt_list_success", model.TaskStatusSuccess, 1)
	createTestPushRecord(t, db, "evt_list_failed", model.TaskStatusFailed, 2)
	createTestPushRecord(t, db, "evt_list_pending", model.TaskStatusPending, 1)

	cases := []struct {
		name      string
		query     string
		wantTotal int64
		wantEvent string
	}{
		{"按状态筛选", "?status=pending", 1, "evt_list_pending"},
		{"按关键字筛选", "?keyword=evt_list_failed", 1, "evt_list_failed"},
		{"按消费端筛选", "?consumer_id=1", 2, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/push-records"+tc.query, nil)
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("列表查询失败: %d %s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Code int `json:"code"`
				Data struct {
					List []struct {
						EventID      string `json:"event_id"`
						Status       string `json:"status"`
						ConsumerName string `json:"consumer_name"`
					} `json:"list"`
					Total int64 `json:"total"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("解析响应失败: %v", err)
			}
			if resp.Code != 0 || resp.Data.Total != tc.wantTotal {
				t.Fatalf("筛选结果错误: %+v, body=%s", resp, rec.Body.String())
			}
			if tc.wantEvent != "" && (len(resp.Data.List) != 1 || resp.Data.List[0].EventID != tc.wantEvent) {
				t.Fatalf("事件 ID 不匹配: %+v", resp.Data.List)
			}
			if len(resp.Data.List) > 0 && resp.Data.List[0].ConsumerName != "测试消费端" {
				t.Fatalf("列表应返回消费端快照: %+v", resp.Data.List[0])
			}
		})
	}
}

func TestGetPushRecordDetail(t *testing.T) {
	engine, db := newPushRecordFixture(t)
	item := createTestPushRecord(t, db, "evt_detail", model.TaskStatusFailed, 1)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/push-records/%d", item.ID), nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("详情查询失败: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			EventID          string `json:"event_id"`
			ConsumerName     string `json:"consumer_name"`
			CallbackURL      string `json:"callback_url"`
			Status           string `json:"status"`
			LastHTTPStatus   int    `json:"last_http_status"`
			LastLatencyMS    int64  `json:"last_latency_ms"`
			RawXML           string `json:"raw_xml"`
			JSONPayload      string `json:"json_payload"`
			RawEncryptedBody string `json:"raw_encrypted_body"`
			InboundQuery     string `json:"inbound_query"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Data.EventID != "evt_detail" || resp.Data.ConsumerName != "测试消费端" ||
		resp.Data.CallbackURL != "http://example.test/callback" || resp.Data.Status != model.TaskStatusFailed ||
		resp.Data.LastHTTPStatus != http.StatusOK || resp.Data.LastLatencyMS != 12 {
		t.Fatalf("详情字段错误: %+v", resp.Data)
	}
	if resp.Data.RawXML == "" || resp.Data.JSONPayload == "" ||
		resp.Data.RawEncryptedBody == "" || resp.Data.InboundQuery == "" {
		t.Fatalf("详情应返回载荷大字段: %+v", resp.Data)
	}
}

func TestPushRecordSnapshotFallbackForDeletedConsumer(t *testing.T) {
	engine, db := newPushRecordFixture(t)
	consumer := &model.Consumer{
		Name:        "已删除消费端",
		CallbackURL: "http://example.test/old-callback",
		ForwardMode: model.ForwardModeJSON,
		AuthType:    model.AuthTypeNone,
		Enabled:     true,
	}
	if err := db.Create(consumer).Error; err != nil {
		t.Fatalf("创建消费端失败: %v", err)
	}
	item := createTestPushRecord(t, db, "evt_snapshot_fallback", model.TaskStatusSuccess, consumer.ID)
	if err := db.Model(item).Updates(map[string]any{"consumer_name": "", "callback_url": ""}).Error; err != nil {
		t.Fatalf("清空快照失败: %v", err)
	}
	if err := db.Delete(consumer).Error; err != nil {
		t.Fatalf("软删除消费端失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/push-records/%d", item.ID), nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("详情查询失败: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			ConsumerName string `json:"consumer_name"`
			CallbackURL  string `json:"callback_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Data.ConsumerName != consumer.Name || resp.Data.CallbackURL != consumer.CallbackURL {
		t.Fatalf("应回查软删除消费端补齐快照: %+v", resp.Data)
	}
}

func TestDeletePushRecord(t *testing.T) {
	engine, db := newPushRecordFixture(t)
	success := createTestPushRecord(t, db, "evt_delete_success", model.TaskStatusSuccess, 1)
	pending := createTestPushRecord(t, db, "evt_delete_pending", model.TaskStatusPending, 1)

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/push-records/%d", success.ID), nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除终态记录应成功: %d %s", rec.Code, rec.Body.String())
	}
	var count int64
	db.Model(&model.RelayTask{}).Where("id = ?", success.ID).Count(&count)
	if count != 0 {
		t.Fatal("终态推送记录应被删除")
	}

	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/push-records/%d", pending.ID), nil)
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("待投递任务删除应返回 409，实际 %d", rec.Code)
	}
	db.Model(&model.RelayTask{}).Where("id = ?", pending.ID).Count(&count)
	if count != 1 {
		t.Fatal("待投递任务不应被删除")
	}

	req = httptest.NewRequest(http.MethodDelete, "/push-records/999999", nil)
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的记录应返回 404，实际 %d", rec.Code)
	}

	var audits int64
	db.Model(&model.AuditLog{}).Where("action = ?", "push_record.delete").Count(&audits)
	if audits != 1 {
		t.Fatalf("删除成功应写审计日志，实际 %d", audits)
	}
}

func TestDeleteBatchPushRecordsSkipsUnfinished(t *testing.T) {
	engine, db := newPushRecordFixture(t)
	success := createTestPushRecord(t, db, "evt_batch_success", model.TaskStatusSuccess, 1)
	failed := createTestPushRecord(t, db, "evt_batch_failed", model.TaskStatusFailed, 1)
	dead := createTestPushRecord(t, db, "evt_batch_dead", model.TaskStatusDead, 1)
	pending := createTestPushRecord(t, db, "evt_batch_pending", model.TaskStatusPending, 1)
	sending := createTestPushRecord(t, db, "evt_batch_sending", model.TaskStatusSending, 1)

	body, _ := json.Marshal(map[string]any{
		"ids": []uint64{success.ID, failed.ID, dead.ID, pending.ID, sending.ID, 999999},
	})
	req := httptest.NewRequest(http.MethodPost, "/push-records/batch-delete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("批量删除应成功: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Total      int64    `json:"total"`
			Deleted    int64    `json:"deleted"`
			Skipped    int64    `json:"skipped"`
			SkippedIDs []uint64 `json:"skipped_ids"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Code != 0 || resp.Data.Total != 6 || resp.Data.Deleted != 3 || resp.Data.Skipped != 3 {
		t.Fatalf("批量删除统计错误: %+v, body=%s", resp.Data, rec.Body.String())
	}
	if len(resp.Data.SkippedIDs) != 2 {
		t.Fatalf("跳过 ID 应包含待投递与投递中: %+v", resp.Data.SkippedIDs)
	}
	for _, id := range []uint64{pending.ID, sending.ID} {
		var count int64
		db.Model(&model.RelayTask{}).Where("id = ?", id).Count(&count)
		if count != 1 {
			t.Fatalf("未结束任务不应被删除，ID=%d", id)
		}
	}
	var audits int64
	db.Model(&model.AuditLog{}).Where("action = ?", "push_record.delete_batch").Count(&audits)
	if audits != 1 {
		t.Fatalf("批量删除应写审计日志，实际 %d", audits)
	}
}
