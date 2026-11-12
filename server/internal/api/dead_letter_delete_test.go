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
	"wecomrelay/internal/relay"
	"wecomrelay/internal/security"
)

func newDeadLetterDeleteFixture(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", url.QueryEscape("dl-delete-"+t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.DeadLetter{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	cipher, err := security.NewCipher("dead-letter-delete-test-key")
	if err != nil {
		t.Fatalf("创建加密器失败: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewDeadLetterHandler(db, cipher, relay.NewSender(logger, false), logger)

	engine := gin.New()
	engine.DELETE("/dead-letters/:id", handler.Delete)
	engine.POST("/dead-letters/batch-delete", handler.DeleteBatch)
	return engine, db
}

func createTestDeadLetter(t *testing.T, db *gorm.DB, eventID, status string) *model.DeadLetter {
	t.Helper()
	item := &model.DeadLetter{
		EventID:      eventID,
		ConsumerID:   1,
		ConsumerName: "测试消费端",
		ForwardMode:  model.ForwardModeJSON,
		RawXML:       "<xml/>",
		JSONPayload:  "{}",
		Status:       status,
	}
	if err := db.Create(item).Error; err != nil {
		t.Fatalf("创建死信失败: %v", err)
	}
	return item
}

func TestDeleteSingleDeadLetter(t *testing.T) {
	engine, db := newDeadLetterDeleteFixture(t)
	item := createTestDeadLetter(t, db, "evt_delete_1", model.DeadStatusPending)

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/dead-letters/%d", item.ID), nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("单条删除应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var count int64
	db.Model(&model.DeadLetter{}).Where("id = ?", item.ID).Count(&count)
	if count != 0 {
		t.Fatalf("死信应被永久删除，实际剩余 %d", count)
	}
}

func TestDeleteReplayingDeadLetterReturnsConflict(t *testing.T) {
	engine, db := newDeadLetterDeleteFixture(t)
	item := createTestDeadLetter(t, db, "evt_delete_replaying", model.DeadStatusReplaying)

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/dead-letters/%d", item.ID), nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("replaying 状态应返回 409，实际 %d", rec.Code)
	}
	var count int64
	db.Model(&model.DeadLetter{}).Where("id = ?", item.ID).Count(&count)
	if count != 1 {
		t.Fatalf("replaying 死信不应被删除")
	}
}

func TestDeleteBatchDeadLettersSkipsReplaying(t *testing.T) {
	engine, db := newDeadLetterDeleteFixture(t)
	pending := createTestDeadLetter(t, db, "evt_batch_pending", model.DeadStatusPending)
	resolved := createTestDeadLetter(t, db, "evt_batch_resolved", model.DeadStatusResolved)
	replaying := createTestDeadLetter(t, db, "evt_batch_replaying", model.DeadStatusReplaying)

	body, _ := json.Marshal(map[string]any{
		"ids": []uint64{pending.ID, resolved.ID, replaying.ID, 999999},
	})
	req := httptest.NewRequest(http.MethodPost, "/dead-letters/batch-delete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("批量删除应成功，实际 %d: %s", rec.Code, rec.Body.String())
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
	if resp.Code != 0 {
		t.Fatalf("响应 code 错误: %d, body=%s", resp.Code, rec.Body.String())
	}
	if resp.Data.Total != 4 || resp.Data.Deleted != 2 || resp.Data.Skipped != 2 {
		t.Fatalf("批量删除统计错误: %+v, body=%s", resp.Data, rec.Body.String())
	}
	if len(resp.Data.SkippedIDs) != 1 || resp.Data.SkippedIDs[0] != replaying.ID {
		t.Fatalf("跳过 ID 应只包含 replaying: %+v", resp.Data.SkippedIDs)
	}

	var replayingCount int64
	db.Model(&model.DeadLetter{}).Where("id = ?", replaying.ID).Count(&replayingCount)
	if replayingCount != 1 {
		t.Fatal("replaying 死信应保留")
	}
}
