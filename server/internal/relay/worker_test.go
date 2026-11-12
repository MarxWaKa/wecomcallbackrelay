package relay

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"wecomrelay/internal/model"
	"wecomrelay/internal/security"
)

type workerFixture struct {
	db     *gorm.DB
	worker *Worker
	cipher *security.Cipher
}

func newWorkerFixture(t *testing.T) *workerFixture {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", url.QueryEscape("worker-"+t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Consumer{}, &model.RelayTask{}, &model.DeadLetter{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	cipher, err := security.NewCipher("worker-test-encrypt-key-123456")
	if err != nil {
		t.Fatalf("创建加密器失败: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := NewWorker(db, cipher, NewSender(logger), logger, time.Second, 2, nil)
	return &workerFixture{db: db, worker: worker, cipher: cipher}
}

func (f *workerFixture) createTask(t *testing.T, callbackURL string, maxRetry int, retryCount int) (*model.Consumer, *model.RelayTask) {
	t.Helper()
	encAuth, _ := f.cipher.Encrypt("{}")
	consumer := &model.Consumer{
		Name:        "worker-test-consumer",
		CallbackURL: callbackURL,
		ForwardMode: model.ForwardModeJSON,
		AuthType:    model.AuthTypeNone,
		AuthConfig:  encAuth,
		Enabled:     true,
		TimeoutMS:   3000,
		MaxRetry:    maxRetry,
	}
	if err := f.db.Create(consumer).Error; err != nil {
		t.Fatalf("创建消费端失败: %v", err)
	}
	task := &model.RelayTask{
		EventID:     "evt_worker_test",
		ConsumerID:  consumer.ID,
		ForwardMode: model.ForwardModeJSON,
		JSONPayload: `{"event_id":"evt_worker_test","payload":{"user_id":"zhangsan"}}`,
		Status:      model.TaskStatusSending,
		RetryCount:  retryCount,
		MaxRetry:    maxRetry,
	}
	if err := f.db.Create(task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	return consumer, task
}

func TestProcessTaskSuccess(t *testing.T) {
	f := newWorkerFixture(t)
	var gotEventID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEventID = r.Header.Get("X-Event-Id")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, task := f.createTask(t, server.URL, 5, 0)
	f.worker.processTask(context.Background(), task.ID)

	var updated model.RelayTask
	if err := f.db.First(&updated, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusSuccess {
		t.Fatalf("任务应成功，实际状态 %s，错误 %s", updated.Status, updated.LastError)
	}
	if gotEventID != "evt_worker_test" {
		t.Fatalf("X-Event-Id 错误: %q", gotEventID)
	}
}

func TestProcessTaskRetriesThenDeadLetter(t *testing.T) {
	f := newWorkerFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "consumer down", http.StatusBadGateway)
	}))
	defer server.Close()

	consumer, task := f.createTask(t, server.URL, 2, 0)

	// 第一次失败：进入 pending 并设置 next_retry_at
	f.worker.processTask(context.Background(), task.ID)
	var first model.RelayTask
	if err := f.db.First(&first, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if first.Status != model.TaskStatusPending || first.RetryCount != 1 || first.NextRetryAt == nil {
		t.Fatalf("首次失败应进入重试，实际: %+v", first)
	}

	// 第二次失败：retry_count 2 -> 再次 pending
	if err := f.db.Model(&model.RelayTask{}).Where("id = ?", task.ID).
		Updates(map[string]any{"status": model.TaskStatusSending}).Error; err != nil {
		t.Fatal(err)
	}
	f.worker.processTask(context.Background(), task.ID)
	var second model.RelayTask
	if err := f.db.First(&second, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if second.Status != model.TaskStatusPending || second.RetryCount != 2 {
		t.Fatalf("第二次失败应继续重试，实际: %+v", second)
	}

	// 第三次失败：超过 max_retry=2，转死信
	if err := f.db.Model(&model.RelayTask{}).Where("id = ?", task.ID).
		Updates(map[string]any{"status": model.TaskStatusSending}).Error; err != nil {
		t.Fatal(err)
	}
	f.worker.processTask(context.Background(), task.ID)
	var dead model.RelayTask
	if err := f.db.First(&dead, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if dead.Status != model.TaskStatusDead || dead.RetryCount != 3 {
		t.Fatalf("超过重试上限应转死信，实际: %+v", dead)
	}

	var letter model.DeadLetter
	if err := f.db.Where("event_id = ? AND consumer_id = ?", task.EventID, consumer.ID).First(&letter).Error; err != nil {
		t.Fatalf("应写入死信: %v", err)
	}
	if letter.Status != model.DeadStatusPending || letter.LastError == "" || letter.JSONPayload == "" {
		t.Fatalf("死信快照不完整: %+v", letter)
	}
	if letter.ConsumerName != consumer.Name || letter.ForwardMode != model.ForwardModeJSON {
		t.Fatalf("死信快照字段错误: %+v", letter)
	}
}

func TestProcessTaskDisabledConsumerMarksFailed(t *testing.T) {
	f := newWorkerFixture(t)
	consumer, task := f.createTask(t, "http://127.0.0.1:1/never", 5, 0)
	if err := f.db.Model(consumer).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}

	f.worker.processTask(context.Background(), task.ID)
	var updated model.RelayTask
	if err := f.db.First(&updated, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed {
		t.Fatalf("禁用消费端的任务应标记 failed，实际 %s", updated.Status)
	}
	var letters int64
	f.db.Model(&model.DeadLetter{}).Count(&letters)
	if letters != 0 {
		t.Fatalf("非重试错误不应产生死信，实际 %d", letters)
	}
}
