package relay

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"wecomrelay/internal/model"
	"wecomrelay/internal/security"
)

const (
	defaultMaxRetry = 5
	claimBatchSize  = 100
	stuckTimeout    = 5 * time.Minute
)

// Worker 轮询 relay_tasks 并异步投递。
type Worker struct {
	db           *gorm.DB
	cipher       *security.Cipher
	sender       *Sender
	logger       *slog.Logger
	interval     time.Duration
	concurrency  int
	backoff      []time.Duration
	logRawEvents bool
	wg           sync.WaitGroup
}

func NewWorker(db *gorm.DB, cipher *security.Cipher, sender *Sender, logger *slog.Logger, interval time.Duration, concurrency int, backoff []time.Duration, logRawEvents bool) *Worker {
	if concurrency < 1 {
		concurrency = 1
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Worker{
		db:           db,
		cipher:       cipher,
		sender:       sender,
		logger:       logger,
		interval:     interval,
		concurrency:  concurrency,
		backoff:      backoff,
		logRawEvents: logRawEvents,
	}
}

// Start 启动任务轮询与维护协程。
func (w *Worker) Start(ctx context.Context) {
	w.recoverStuckTasks()
	w.wg.Add(2)
	go w.loop(ctx)
	go w.maintenance(ctx)
}

// Wait 等待后台协程退出。
func (w *Worker) Wait() {
	w.wg.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	defer w.wg.Done()
	w.runOnce(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) {
	tasks, err := w.claimTasks()
	if err != nil {
		w.logger.Error("抢占投递任务失败", "error", err)
		return
	}
	if len(tasks) == 0 {
		return
	}

	sem := make(chan struct{}, w.concurrency)
	var wg sync.WaitGroup
	for i, task := range tasks {
		select {
		case <-ctx.Done():
			w.releaseToPending(tasks[i:])
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(task model.RelayTask) {
			defer wg.Done()
			defer func() { <-sem }()
			w.processTask(ctx, task.ID)
		}(task)
	}
	wg.Wait()
}

// claimTasks 使用 SELECT ... FOR UPDATE SKIP LOCKED 抢占待投递任务，支持多实例部署。
func (w *Worker) claimTasks() ([]model.RelayTask, error) {
	var tasks []model.RelayTask
	now := time.Now()
	err := w.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)", model.TaskStatusPending, now).
			Order("id ASC").
			Limit(claimBatchSize).
			Find(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(tasks))
		for _, task := range tasks {
			ids = append(ids, task.ID)
		}
		return tx.Model(&model.RelayTask{}).
			Where("id IN ?", ids).
			Update("status", model.TaskStatusSending).Error
	})
	return tasks, err
}

func (w *Worker) processTask(ctx context.Context, taskID uint64) {
	var task model.RelayTask
	if err := w.db.First(&task, taskID).Error; err != nil {
		w.logger.Error("读取投递任务失败", "task_id", taskID, "error", err)
		return
	}
	if task.Status != model.TaskStatusSending {
		return
	}

	var consumer model.Consumer
	if err := w.db.First(&consumer, task.ConsumerID).Error; err != nil {
		w.markFailed(task, "消费端不存在或已删除")
		return
	}
	if !consumer.Enabled {
		w.markFailed(task, "消费端已禁用")
		return
	}

	target := Target{
		ConsumerID:  consumer.ID,
		Name:        consumer.Name,
		CallbackURL: consumer.CallbackURL,
		ForwardMode: task.ForwardMode,
		TimeoutMS:   consumer.TimeoutMS,
	}

	var statusCode int
	var elapsed time.Duration
	var err error

	if task.ForwardMode == model.ForwardModeWecomProxy {
		if strings.TrimSpace(task.RawEncryptedBody) == "" || strings.TrimSpace(task.InboundQuery) == "" {
			w.markFailed(task, "原样透传任务缺少原始加密报文或 Query 快照")
			return
		}
		w.logger.Debug("企微原样透传载荷",
			"event_id", task.EventID,
			"consumer_id", consumer.ID,
			"consumer", consumer.Name,
			"agent_id", task.AgentID,
			"inbound_query", task.InboundQuery,
			"body_len", len(task.RawEncryptedBody),
			"body_md5", security.ContentMD5([]byte(task.RawEncryptedBody)),
			"body_sha256", security.ContentHash([]byte(task.RawEncryptedBody)),
			"query_md5", security.ContentMD5([]byte(task.InboundQuery)),
			"query_sha256", security.ContentHash([]byte(task.InboundQuery)),
			"raw_encrypted_body", task.RawEncryptedBody,
		)
		statusCode, elapsed, err = w.sender.DeliverProxy(ctx, target, task.RawEncryptedBody, task.InboundQuery)
	} else {
		var authConfig, wecomToken, wecomAESKey string
		if authConfig, err = w.cipher.Decrypt(consumer.AuthConfig); err != nil {
			w.markFailed(task, "消费端认证配置解密失败: "+err.Error())
			return
		}
		if wecomToken, err = w.cipher.Decrypt(consumer.WecomToken); err != nil {
			w.markFailed(task, "消费端 Token 解密失败: "+err.Error())
			return
		}
		if wecomAESKey, err = w.cipher.Decrypt(consumer.WecomEncodingAESKey); err != nil {
			w.markFailed(task, "消费端 EncodingAESKey 解密失败: "+err.Error())
			return
		}

		target.AuthType = consumer.AuthType
		target.AuthConfig = authConfig
		target.WecomToken = wecomToken
		target.WecomEncodingAESKey = wecomAESKey
		target.WecomCorpID = consumer.WecomCorpID

		// 重新加密是发给消费端的应用：优先使用消费端配置的 AgentID，
		// 未配置时回退到入站企微报文中的 AgentID。
		agentID := task.AgentID
		if consumer.WecomAgentID != "" {
			agentID = consumer.WecomAgentID
		}
		payload := Payload{
			EventID:     task.EventID,
			AgentID:     agentID,
			RawXML:      task.RawXML,
			JSONPayload: task.JSONPayload,
		}

		if task.ForwardMode == model.ForwardModeWecomXML {
			attrs := []any{
				"event_id", task.EventID,
				"consumer_id", consumer.ID,
				"consumer", consumer.Name,
				"agent_id", agentID,
				"corp_id", consumer.WecomCorpID,
				"key_fingerprint", security.KeyFingerprint(wecomToken, wecomAESKey),
				"raw_xml_len", len(task.RawXML),
				"raw_xml_sha256", security.ContentHash([]byte(task.RawXML)),
			}
			if w.logRawEvents {
				attrs = append(attrs, "raw_xml", task.RawXML)
			}
			w.logger.Debug("企微兼容模式投递载荷", attrs...)
		}

		statusCode, elapsed, err = w.sender.Deliver(ctx, target, payload)
	}
	if err == nil {
		w.db.Model(&model.RelayTask{}).
			Where("id = ? AND status = ?", task.ID, model.TaskStatusSending).
			Updates(map[string]any{"status": model.TaskStatusSuccess, "last_error": ""})
		w.logger.Info("投递成功",
			"event_id", task.EventID,
			"consumer_id", consumer.ID,
			"consumer", consumer.Name,
			"mode", task.ForwardMode,
			"http_status", statusCode,
			"latency_ms", elapsed.Milliseconds(),
			"retry", task.RetryCount,
		)
		return
	}

	w.logger.Warn("投递失败",
		"event_id", task.EventID,
		"consumer_id", consumer.ID,
		"consumer", consumer.Name,
		"mode", task.ForwardMode,
		"http_status", statusCode,
		"latency_ms", elapsed.Milliseconds(),
		"retry", task.RetryCount,
		"error", err,
	)
	w.handleFailure(task, consumer, target, statusCode, err)
}

func (w *Worker) handleFailure(task model.RelayTask, consumer model.Consumer, target Target, statusCode int, deliverErr error) {
	// Sender 的错误信息已包含 HTTP 状态码，避免重复拼接。
	lastError := deliverErr.Error()
	if len(lastError) > 2000 {
		lastError = lastError[:2000]
	}

	newRetry := task.RetryCount + 1
	maxRetry := task.MaxRetry
	if maxRetry <= 0 {
		maxRetry = defaultMaxRetry
	}
	if newRetry > maxRetry {
		w.toDeadLetter(task, consumer, target, newRetry, lastError)
		return
	}

	nextRetryAt := time.Now().Add(BackoffFor(w.backoff, newRetry))
	err := w.db.Model(&model.RelayTask{}).
		Where("id = ? AND status = ?", task.ID, model.TaskStatusSending).
		Updates(map[string]any{
			"status":        model.TaskStatusPending,
			"retry_count":   newRetry,
			"next_retry_at": nextRetryAt,
			"last_error":    lastError,
		}).Error
	if err != nil {
		w.logger.Error("更新重试任务失败", "task_id", task.ID, "error", err)
	}
}

func (w *Worker) toDeadLetter(task model.RelayTask, consumer model.Consumer, target Target, retryCount int, lastError string) {
	deadAgentID := task.AgentID
	if task.ForwardMode == model.ForwardModeWecomXML && consumer.WecomAgentID != "" {
		deadAgentID = consumer.WecomAgentID
	}
	err := w.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.RelayTask{}).
			Where("id = ? AND status = ?", task.ID, model.TaskStatusSending).
			Updates(map[string]any{
				"status":        model.TaskStatusDead,
				"retry_count":   retryCount,
				"next_retry_at": nil,
				"last_error":    lastError,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		return tx.Create(&model.DeadLetter{
			EventID:             task.EventID,
			ConsumerID:          consumer.ID,
			ConsumerName:        consumer.Name,
			ForwardMode:         task.ForwardMode,
			AgentID:             deadAgentID,
			CallbackURL:         consumer.CallbackURL,
			RawXML:              task.RawXML,
			RawEncryptedBody:    task.RawEncryptedBody,
			InboundQuery:        task.InboundQuery,
			JSONPayload:         task.JSONPayload,
			AuthType:            consumer.AuthType,
			AuthConfig:          consumer.AuthConfig,
			WecomTokenSnapshot:  consumer.WecomToken,
			WecomAESKeySnapshot: consumer.WecomEncodingAESKey,
			WecomCorpID:         consumer.WecomCorpID,
			TimeoutMS:           consumer.TimeoutMS,
			LastError:           lastError,
			RetryCount:          retryCount,
			Status:              model.DeadStatusPending,
		}).Error
	})
	if err != nil {
		w.logger.Error("转入死信失败", "task_id", task.ID, "error", err)
		return
	}
	w.logger.Error("投递重试耗尽，已转入死信",
		"event_id", task.EventID,
		"consumer_id", consumer.ID,
		"consumer", consumer.Name,
		"mode", task.ForwardMode,
		"retry_count", retryCount,
	)
}

func (w *Worker) markFailed(task model.RelayTask, reason string) {
	if len(reason) > 2000 {
		reason = reason[:2000]
	}
	err := w.db.Model(&model.RelayTask{}).
		Where("id = ? AND status = ?", task.ID, model.TaskStatusSending).
		Updates(map[string]any{
			"status":     model.TaskStatusFailed,
			"last_error": reason,
		}).Error
	if err != nil {
		w.logger.Error("标记任务失败状态出错", "task_id", task.ID, "error", err)
		return
	}
	w.logger.Warn("投递任务跳过", "event_id", task.EventID, "consumer_id", task.ConsumerID, "reason", reason)
}

// releaseToPending 在服务关闭时把尚未开始处理的任务放回待投递状态。
func (w *Worker) releaseToPending(tasks []model.RelayTask) {
	ids := make([]uint64, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	if len(ids) == 0 {
		return
	}
	if err := w.db.Model(&model.RelayTask{}).
		Where("id IN ? AND status = ?", ids, model.TaskStatusSending).
		Update("status", model.TaskStatusPending).Error; err != nil {
		w.logger.Error("释放未处理任务失败", "error", err)
	}
}

// recoverStuckTasks 将异常中断（如进程崩溃）遗留在 sending 状态的任务恢复为 pending。
func (w *Worker) recoverStuckTasks() {
	cutoff := time.Now().Add(-stuckTimeout)
	result := w.db.Model(&model.RelayTask{}).
		Where("status = ? AND updated_at < ?", model.TaskStatusSending, cutoff).
		Updates(map[string]any{
			"status":     model.TaskStatusPending,
			"last_error": "服务重启恢复：上一次投递未完成",
		})
	if result.Error != nil {
		w.logger.Error("恢复中断任务失败", "error", result.Error)
	} else if result.RowsAffected > 0 {
		w.logger.Warn("已恢复中断任务", "count", result.RowsAffected)
	}
}

func (w *Worker) maintenance(ctx context.Context) {
	defer w.wg.Done()
	w.checkDeadLetterAlerts()

	alertTicker := time.NewTicker(10 * time.Minute)
	defer alertTicker.Stop()
	cleanupTicker := time.NewTicker(24 * time.Hour)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-alertTicker.C:
			w.checkDeadLetterAlerts()
		case <-cleanupTicker.C:
			w.cleanup()
		}
	}
}

func (w *Worker) checkDeadLetterAlerts() {
	var pending int64
	if err := w.db.Model(&model.DeadLetter{}).
		Where("status = ?", model.DeadStatusPending).
		Count(&pending).Error; err != nil {
		w.logger.Error("统计死信数量失败", "error", err)
		return
	}
	if pending == 0 {
		return
	}

	var oldest model.DeadLetter
	if err := w.db.Where("status = ?", model.DeadStatusPending).
		Order("created_at ASC").
		First(&oldest).Error; err != nil {
		w.logger.Error("读取最老死信失败", "error", err)
		return
	}
	if pending > 10 || time.Since(oldest.CreatedAt) > time.Hour {
		w.logger.Error("死信积压告警",
			"pending", pending,
			"oldest_minutes", int64(time.Since(oldest.CreatedAt).Minutes()),
		)
	}
}

// cleanup 清理过期死信与已完成任务：resolved 30 天、discarded 7 天、任务 30 天。
func (w *Worker) cleanup() {
	now := time.Now()
	if err := w.db.Where("status = ? AND resolved_at < ?", model.DeadStatusResolved, now.AddDate(0, 0, -30)).
		Delete(&model.DeadLetter{}).Error; err != nil {
		w.logger.Error("清理已解决死信失败", "error", err)
	}
	if err := w.db.Where("status = ? AND created_at < ?", model.DeadStatusDiscarded, now.AddDate(0, 0, -7)).
		Delete(&model.DeadLetter{}).Error; err != nil {
		w.logger.Error("清理已丢弃死信失败", "error", err)
	}
	if err := w.db.Where("status IN ? AND updated_at < ?", []string{model.TaskStatusSuccess, model.TaskStatusFailed, model.TaskStatusDead}, now.AddDate(0, 0, -30)).
		Delete(&model.RelayTask{}).Error; err != nil {
		w.logger.Error("清理历史任务失败", "error", err)
	}
}
