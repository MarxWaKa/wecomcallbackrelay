package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/model"
	"wecomrelay/internal/relay"
	"wecomrelay/internal/security"
)

const batchReplayConcurrency = 5

var (
	errDeadLetterNotFound     = errors.New("死信不存在")
	errDeadLetterBusy         = errors.New("死信正在重推，请勿重复操作")
	errDeadLetterState        = errors.New("当前状态不允许该操作")
	errReplayConsumerDeleted  = errors.New("消费端已删除，无法使用最新配置重推；可勾选“使用死信快照配置”后重试")
	errReplayConsumerDisabled = errors.New("消费端已禁用，无法使用最新配置重推；可勾选“使用死信快照配置”后重试")
	errReplayConsumerMissing  = errors.New("消费端不存在，无法使用最新配置重推；可勾选“使用死信快照配置”后重试")
	errReplayPersist          = errors.New("重推结果回写失败")
)

// DeadLetterHandler 死信查询与人工重推。
type DeadLetterHandler struct {
	db     *gorm.DB
	cipher *security.Cipher
	sender *relay.Sender
	logger *slog.Logger
}

func NewDeadLetterHandler(db *gorm.DB, cipher *security.Cipher, sender *relay.Sender, logger *slog.Logger) *DeadLetterHandler {
	return &DeadLetterHandler{db: db, cipher: cipher, sender: sender, logger: logger}
}

type deadLetterView struct {
	ID           uint64     `json:"id"`
	EventID      string     `json:"event_id"`
	ConsumerID   uint64     `json:"consumer_id"`
	ConsumerName string     `json:"consumer_name"`
	ForwardMode  string     `json:"forward_mode"`
	CallbackURL  string     `json:"callback_url"`
	Status       string     `json:"status"`
	RetryCount   int        `json:"retry_count"`
	LastError    string     `json:"last_error"`
	CreatedAt    time.Time  `json:"created_at"`
	ResolvedAt   *time.Time `json:"resolved_at"`
}

type deadLetterConsumerState struct {
	ConsumerExists      bool   `json:"consumer_exists"`
	ConsumerDeleted     bool   `json:"consumer_deleted"`
	ConsumerEnabled     bool   `json:"consumer_enabled"`
	CurrentConsumerName string `json:"current_consumer_name"`
	CurrentCallbackURL  string `json:"current_callback_url"`
	CurrentForwardMode  string `json:"current_forward_mode"`
}

type deadLetterDetail struct {
	deadLetterView
	deadLetterConsumerState
	RawXML              string `json:"raw_xml"`
	RawEncryptedBody    string `json:"raw_encrypted_body"`
	InboundQuery        string `json:"inbound_query"`
	JSONPayload         string `json:"json_payload"`
	AuthType            string `json:"auth_type"`
	WecomTokenMasked    string `json:"wecom_token_masked"`
	WecomAESKeyMasked   string `json:"wecom_encoding_aes_key_masked"`
	WecomKeyFingerprint string `json:"wecom_key_fingerprint"`
	WecomCorpID         string `json:"wecom_corp_id"`
	WecomAgentID        string `json:"wecom_agent_id"`
	TimeoutMS           int    `json:"timeout_ms"`
}

type replayPlan struct {
	Target       relay.Target
	Payload      relay.Payload
	IsProxy      bool
	RawBody      string
	RawQuery     string
	ConfigSource string
	CallbackURL  string
}

type replayOutcome struct {
	Status       string
	ConfigSource string
	StatusCode   int
	LatencyMS    int64
	CallbackURL  string
	TaskSynced   bool
}

// List 死信列表（不返回大字段）。
func (h *DeadLetterHandler) List(c *gin.Context) {
	page, size := PageParams(c)
	query := h.db.Model(&model.DeadLetter{})

	if raw := strings.TrimSpace(c.Query("consumer_id")); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			Fail(c, http.StatusBadRequest, "消费端 ID 不合法")
			return
		}
		query = query.Where("consumer_id = ?", id)
	}
	if mode := strings.TrimSpace(c.Query("forward_mode")); mode != "" {
		query = query.Where("forward_mode = ?", mode)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("event_id LIKE ? OR consumer_name LIKE ?", like, like)
	}
	if start, ok := parseQueryTime(c.Query("start_time"), false); ok {
		query = query.Where("created_at >= ?", start)
	}
	if end, ok := parseQueryTime(c.Query("end_time"), true); ok {
		query = query.Where("created_at <= ?", end)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询死信失败")
		return
	}
	var items []model.DeadLetter
	if err := query.
		Select("id, event_id, consumer_id, consumer_name, forward_mode, callback_url, status, retry_count, last_error, created_at, resolved_at").
		Order("id DESC").
		Offset((page - 1) * size).
		Limit(size).
		Find(&items).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询死信失败")
		return
	}
	views := make([]deadLetterView, 0, len(items))
	for _, item := range items {
		views = append(views, toDeadLetterView(item))
	}
	OK(c, PageData{List: views, Total: total, Page: page, PageSize: size})
}

// Get 死信详情。
func (h *DeadLetterHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.DeadLetter
	if err := h.db.First(&item, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "死信不存在")
		return
	}
	wecomToken, _ := h.cipher.Decrypt(item.WecomTokenSnapshot)
	aesKey, _ := h.cipher.Decrypt(item.WecomAESKeySnapshot)
	OK(c, deadLetterDetail{
		deadLetterView:          toDeadLetterView(item),
		deadLetterConsumerState: h.currentConsumerState(item.ConsumerID),
		RawXML:                  item.RawXML,
		RawEncryptedBody:        item.RawEncryptedBody,
		InboundQuery:            item.InboundQuery,
		JSONPayload:             item.JSONPayload,
		AuthType:                item.AuthType,
		WecomTokenMasked:        security.MaskSecret(wecomToken),
		WecomAESKeyMasked:       security.MaskSecret(aesKey),
		WecomKeyFingerprint:     security.KeyFingerprint(wecomToken, aesKey),
		WecomCorpID:             item.WecomCorpID,
		WecomAgentID:            item.AgentID,
		TimeoutMS:               item.TimeoutMS,
	})
}

// Replay 单条重推。默认使用消费端最新配置，use_snapshot=true 时使用死信快照。
func (h *DeadLetterHandler) Replay(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	useSnapshot, ok := parseUseSnapshot(c)
	if !ok {
		return
	}

	outcome, err := h.replayOne(context.Background(), id, useSnapshot)
	source := "latest"
	if useSnapshot {
		source = "snapshot"
	}
	switch {
	case errors.Is(err, errDeadLetterNotFound):
		Fail(c, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, errDeadLetterBusy),
		errors.Is(err, errDeadLetterState),
		errors.Is(err, errReplayConsumerDeleted),
		errors.Is(err, errReplayConsumerDisabled),
		errors.Is(err, errReplayConsumerMissing):
		Fail(c, http.StatusConflict, err.Error())
		return
	case errors.Is(err, errReplayPersist):
		writeAudit(h.db, c, "dead_letter.replay", "dead_letter", id,
			truncateMessage("单条重推失败（配置来源："+source+"）: "+err.Error(), 500), "failed")
		Fail(c, http.StatusInternalServerError, "重推结果回写失败")
		return
	case err != nil:
		writeAudit(h.db, c, "dead_letter.replay", "dead_letter", id,
			truncateMessage("单条重推失败（配置来源："+source+"）: "+err.Error(), 500), "failed")
		Fail(c, http.StatusBadGateway, "重推失败: "+err.Error())
		return
	}
	writeAudit(h.db, c, "dead_letter.replay", "dead_letter", id,
		fmt.Sprintf("单条重推成功（配置来源：%s，目标：%s，HTTP %d，耗时 %dms，task_synced=%t）",
			outcome.ConfigSource, outcome.CallbackURL, outcome.StatusCode, outcome.LatencyMS, outcome.TaskSynced), "success")
	OK(c, gin.H{
		"status":        outcome.Status,
		"config_source": outcome.ConfigSource,
		"http_status":   outcome.StatusCode,
		"latency_ms":    outcome.LatencyMS,
		"task_synced":   outcome.TaskSynced,
	})
}

type batchReplayRequest struct {
	IDs         []uint64 `json:"ids"`
	UseSnapshot bool     `json:"use_snapshot"`
}

// ReplayBatch 批量重推，单次上限 50 条。
func (h *DeadLetterHandler) ReplayBatch(c *gin.Context) {
	var req batchReplayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	if len(req.IDs) == 0 {
		Fail(c, http.StatusBadRequest, "请选择要重推的死信")
		return
	}
	if len(req.IDs) > 50 {
		Fail(c, http.StatusBadRequest, "单次最多重推 50 条")
		return
	}

	seen := make(map[uint64]struct{}, len(req.IDs))
	uniqueIDs := make([]uint64, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id == 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 {
		Fail(c, http.StatusBadRequest, "请选择要重推的死信")
		return
	}

	type replayResult struct {
		id      uint64
		outcome replayOutcome
		err     error
	}
	resultCh := make(chan replayResult, len(uniqueIDs))
	sem := make(chan struct{}, batchReplayConcurrency)
	var wg sync.WaitGroup
	for _, id := range uniqueIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func(id uint64) {
			defer wg.Done()
			defer func() { <-sem }()
			outcome, err := h.replayOne(context.Background(), id, req.UseSnapshot)
			resultCh <- replayResult{id: id, outcome: outcome, err: err}
		}(id)
	}
	wg.Wait()
	close(resultCh)

	successCount, failedCount := 0, 0
	results := make([]gin.H, 0, len(uniqueIDs))
	for item := range resultCh {
		if item.err != nil {
			failedCount++
			results = append(results, gin.H{"id": item.id, "success": false, "message": item.err.Error()})
			continue
		}
		successCount++
		results = append(results, gin.H{
			"id":            item.id,
			"success":       true,
			"status":        item.outcome.Status,
			"config_source": item.outcome.ConfigSource,
			"http_status":   item.outcome.StatusCode,
			"latency_ms":    item.outcome.LatencyMS,
			"task_synced":   item.outcome.TaskSynced,
		})
	}
	auditResult := "success"
	if successCount == 0 {
		auditResult = "failed"
	} else if failedCount > 0 {
		auditResult = "partial"
	}
	sourceLabel := "latest"
	if req.UseSnapshot {
		sourceLabel = "snapshot"
	}
	writeAudit(h.db, c, "dead_letter.replay_batch", "dead_letter", 0,
		"批量重推，成功 "+strconv.Itoa(successCount)+" 条，失败 "+strconv.Itoa(failedCount)+
			" 条，配置来源："+sourceLabel, auditResult)
	OK(c, gin.H{
		"total":   len(uniqueIDs),
		"success": successCount,
		"failed":  failedCount,
		"result":  auditResult,
		"results": results,
	})
}

// Discard 标记不再重推。
func (h *DeadLetterHandler) Discard(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	result := h.db.Model(&model.DeadLetter{}).
		Where("id = ? AND status = ?", id, model.DeadStatusPending).
		Update("status", model.DeadStatusDiscarded)
	if result.Error != nil {
		Fail(c, http.StatusInternalServerError, "标记丢弃失败")
		return
	}
	if result.RowsAffected == 0 {
		var item model.DeadLetter
		if err := h.db.First(&item, id).Error; err != nil {
			Fail(c, http.StatusNotFound, "死信不存在")
			return
		}
		Fail(c, http.StatusConflict, "当前状态不允许标记丢弃")
		return
	}
	writeAudit(h.db, c, "dead_letter.discard", "dead_letter", id, "标记不再重推", "success")
	OK(c, gin.H{"message": "已标记为不再重推"})
}

// Delete 永久删除单条死信。
func (h *DeadLetterHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.DeadLetter
	if err := h.db.First(&item, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "死信不存在")
		return
	}
	if item.Status == model.DeadStatusReplaying {
		Fail(c, http.StatusConflict, "死信正在重推，请稍后再删除")
		return
	}
	// 条件删除避免“检查后被重推抢占”的竞态。
	result := h.db.Where("id = ? AND status <> ?", id, model.DeadStatusReplaying).Delete(&model.DeadLetter{})
	if result.Error != nil {
		Fail(c, http.StatusInternalServerError, "删除死信失败")
		return
	}
	if result.RowsAffected == 0 {
		Fail(c, http.StatusConflict, "死信正在重推，请稍后再删除")
		return
	}
	writeAudit(h.db, c, "dead_letter.delete", "dead_letter", item.ID, "永久删除死信 "+item.EventID, "success")
	OK(c, gin.H{"message": "已删除"})
}

type batchDeleteRequest struct {
	IDs []uint64 `json:"ids"`
}

// DeleteBatch 批量永久删除死信，单次上限 100 条；replaying 状态会被跳过。
func (h *DeadLetterHandler) DeleteBatch(c *gin.Context) {
	var req batchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	if len(req.IDs) == 0 {
		Fail(c, http.StatusBadRequest, "请选择要删除的死信")
		return
	}
	if len(req.IDs) > 100 {
		Fail(c, http.StatusBadRequest, "单次最多删除 100 条")
		return
	}

	seen := make(map[uint64]struct{}, len(req.IDs))
	ids := make([]uint64, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id == 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		Fail(c, http.StatusBadRequest, "请选择要删除的死信")
		return
	}

	var replayingIDs []uint64
	if err := h.db.Model(&model.DeadLetter{}).
		Where("id IN ? AND status = ?", ids, model.DeadStatusReplaying).
		Pluck("id", &replayingIDs).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询死信状态失败")
		return
	}

	result := h.db.Where("id IN ? AND status <> ?", ids, model.DeadStatusReplaying).
		Delete(&model.DeadLetter{})
	if result.Error != nil {
		Fail(c, http.StatusInternalServerError, "批量删除死信失败")
		return
	}
	skipped := int64(len(ids)) - result.RowsAffected
	writeAudit(h.db, c, "dead_letter.delete_batch", "dead_letter", 0,
		"批量永久删除死信，共 "+strconv.Itoa(len(ids))+" 条，删除 "+strconv.FormatInt(result.RowsAffected, 10)+" 条", "success")
	OK(c, gin.H{
		"total":       len(ids),
		"deleted":     result.RowsAffected,
		"skipped":     skipped,
		"skipped_ids": replayingIDs,
	})
}

// replayOne 执行单条死信重推，返回最终状态与投递结果。
func (h *DeadLetterHandler) replayOne(ctx context.Context, id uint64, useSnapshot bool) (replayOutcome, error) {
	if id == 0 {
		return replayOutcome{}, errDeadLetterNotFound
	}
	var item model.DeadLetter
	if err := h.db.First(&item, id).Error; err != nil {
		return replayOutcome{}, errDeadLetterNotFound
	}
	if item.Status == model.DeadStatusReplaying {
		return replayOutcome{}, errDeadLetterBusy
	}
	if item.Status != model.DeadStatusPending && item.Status != model.DeadStatusDiscarded {
		return replayOutcome{}, errDeadLetterState
	}

	var consumer *model.Consumer
	if !useSnapshot {
		var err error
		consumer, err = h.loadReplayConsumer(item.ConsumerID)
		if err != nil {
			return replayOutcome{}, err
		}
	}

	plan, err := h.buildReplayPlan(item, consumer, useSnapshot)
	if err != nil {
		return replayOutcome{}, err
	}

	claim := h.db.Model(&model.DeadLetter{}).
		Where("id = ? AND status IN ?", id, []string{model.DeadStatusPending, model.DeadStatusDiscarded}).
		Update("status", model.DeadStatusReplaying)
	if claim.Error != nil {
		return replayOutcome{}, claim.Error
	}
	if claim.RowsAffected == 0 {
		var current model.DeadLetter
		if err := h.db.First(&current, id).Error; err != nil {
			return replayOutcome{}, errDeadLetterNotFound
		}
		if current.Status == model.DeadStatusReplaying {
			return replayOutcome{}, errDeadLetterBusy
		}
		return replayOutcome{}, errDeadLetterState
	}

	var statusCode int
	var elapsed time.Duration
	var deliverErr error
	if plan.IsProxy {
		statusCode, elapsed, deliverErr = h.sender.DeliverProxy(ctx, plan.Target, plan.RawBody, plan.RawQuery)
	} else {
		statusCode, elapsed, deliverErr = h.sender.Deliver(ctx, plan.Target, plan.Payload)
	}

	outcome := replayOutcome{
		ConfigSource: plan.ConfigSource,
		StatusCode:   statusCode,
		LatencyMS:    elapsed.Milliseconds(),
		CallbackURL:  plan.CallbackURL,
	}
	if deliverErr != nil {
		status, taskSynced, finishErr := h.finishReplay(item, false, statusCode, elapsed, deliverErr.Error())
		outcome.Status = status
		outcome.TaskSynced = taskSynced
		if finishErr != nil {
			return outcome, fmt.Errorf("%w: %v", errReplayPersist, finishErr)
		}
		return outcome, deliverErr
	}

	status, taskSynced, finishErr := h.finishReplay(item, true, statusCode, elapsed, "")
	outcome.Status = status
	outcome.TaskSynced = taskSynced
	if finishErr != nil {
		return outcome, fmt.Errorf("%w: %v", errReplayPersist, finishErr)
	}
	h.logger.Info("死信重推成功",
		"dead_letter_id", item.ID,
		"event_id", item.EventID,
		"consumer_id", item.ConsumerID,
		"config_source", plan.ConfigSource,
		"http_status", statusCode,
		"latency_ms", elapsed.Milliseconds(),
		"task_synced", taskSynced,
	)
	return outcome, nil
}

// loadReplayConsumer 加载用于重推的最新消费端，并区分删除/禁用/不存在。
func (h *DeadLetterHandler) loadReplayConsumer(id uint64) (*model.Consumer, error) {
	if id == 0 {
		return nil, errReplayConsumerMissing
	}
	var consumer model.Consumer
	err := h.db.First(&consumer, id).Error
	if err == nil {
		if !consumer.Enabled {
			return nil, errReplayConsumerDisabled
		}
		return &consumer, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var deleted model.Consumer
	if err := h.db.Unscoped().First(&deleted, id).Error; err == nil {
		return nil, errReplayConsumerDeleted
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return nil, errReplayConsumerMissing
}

// buildReplayPlan 按最新消费端或死信快照构造投递目标。
// ForwardMode 始终使用死信快照，避免切换模式后载荷不兼容。
func (h *DeadLetterHandler) buildReplayPlan(item model.DeadLetter, consumer *model.Consumer, useSnapshot bool) (replayPlan, error) {
	plan := replayPlan{
		ConfigSource: "latest",
		Target: relay.Target{
			ConsumerID:  item.ConsumerID,
			ForwardMode: item.ForwardMode,
		},
		Payload: relay.Payload{
			EventID:     item.EventID,
			AgentID:     item.AgentID,
			RawXML:      item.RawXML,
			JSONPayload: item.JSONPayload,
		},
	}
	if useSnapshot {
		plan.ConfigSource = "snapshot"
		plan.Target.Name = item.ConsumerName
		plan.Target.CallbackURL = item.CallbackURL
		plan.Target.TimeoutMS = item.TimeoutMS
	} else {
		if consumer == nil {
			return plan, errReplayConsumerMissing
		}
		plan.Target.Name = consumer.Name
		plan.Target.CallbackURL = consumer.CallbackURL
		plan.Target.TimeoutMS = consumer.TimeoutMS
		if consumer.WecomAgentID != "" {
			plan.Payload.AgentID = consumer.WecomAgentID
		}
	}
	plan.CallbackURL = plan.Target.CallbackURL

	if item.ForwardMode == model.ForwardModeWecomProxy {
		if strings.TrimSpace(item.RawEncryptedBody) == "" || strings.TrimSpace(item.InboundQuery) == "" {
			return plan, errors.New("该死信缺少原始加密报文快照，无法原样透传")
		}
		plan.IsProxy = true
		plan.RawBody = item.RawEncryptedBody
		plan.RawQuery = item.InboundQuery
		return plan, nil
	}

	if useSnapshot {
		if item.ForwardMode == model.ForwardModeWecomXML {
			wecomToken, err := h.cipher.Decrypt(item.WecomTokenSnapshot)
			if err != nil {
				return plan, errors.New("Token 解密失败: " + err.Error())
			}
			wecomAESKey, err := h.cipher.Decrypt(item.WecomAESKeySnapshot)
			if err != nil {
				return plan, errors.New("EncodingAESKey 解密失败: " + err.Error())
			}
			plan.Target.WecomToken = wecomToken
			plan.Target.WecomEncodingAESKey = wecomAESKey
			plan.Target.WecomCorpID = item.WecomCorpID
			return plan, nil
		}
		authConfig, err := h.cipher.Decrypt(item.AuthConfig)
		if err != nil {
			return plan, errors.New("认证配置解密失败: " + err.Error())
		}
		plan.Target.AuthType = item.AuthType
		plan.Target.AuthConfig = authConfig
		return plan, nil
	}

	if item.ForwardMode == model.ForwardModeWecomXML {
		wecomToken, err := h.cipher.Decrypt(consumer.WecomToken)
		if err != nil {
			return plan, errors.New("消费端 Token 解密失败: " + err.Error())
		}
		wecomAESKey, err := h.cipher.Decrypt(consumer.WecomEncodingAESKey)
		if err != nil {
			return plan, errors.New("消费端 EncodingAESKey 解密失败: " + err.Error())
		}
		plan.Target.WecomToken = wecomToken
		plan.Target.WecomEncodingAESKey = wecomAESKey
		plan.Target.WecomCorpID = consumer.WecomCorpID
		return plan, nil
	}

	authConfig, err := h.cipher.Decrypt(consumer.AuthConfig)
	if err != nil {
		return plan, errors.New("消费端认证配置解密失败: " + err.Error())
	}
	plan.Target.AuthType = consumer.AuthType
	plan.Target.AuthConfig = authConfig
	return plan, nil
}

func (h *DeadLetterHandler) finishReplay(item model.DeadLetter, success bool, statusCode int, elapsed time.Duration, message string) (string, bool, error) {
	if success {
		var taskSynced bool
		err := h.db.Transaction(func(tx *gorm.DB) error {
			result := tx.Model(&model.DeadLetter{}).
				Where("id = ? AND status = ?", item.ID, model.DeadStatusReplaying).
				Updates(map[string]any{
					"status":      model.DeadStatusResolved,
					"resolved_at": time.Now(),
					"last_error":  "",
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return errDeadLetterState
			}

			task := tx.Model(&model.RelayTask{}).
				Where("event_id = ? AND consumer_id = ? AND status = ?",
					item.EventID, item.ConsumerID, model.TaskStatusDead).
				Updates(map[string]any{
					"status":           model.TaskStatusSuccess,
					"last_error":       "",
					"next_retry_at":    nil,
					"last_http_status": statusCode,
					"last_latency_ms":  elapsed.Milliseconds(),
				})
			if task.Error != nil {
				// 投递已成功，推送记录回写失败只记录告警，不回滚死信 resolved，
				// 避免把一次成功投递误标为失败并触发重复重推。
				h.logger.Warn("死信重推成功但回写推送记录失败",
					"dead_letter_id", item.ID, "event_id", item.EventID, "consumer_id", item.ConsumerID,
					"error", task.Error)
			} else if task.RowsAffected == 0 {
				h.logger.Warn("死信重推成功但未找到可回写的推送记录",
					"dead_letter_id", item.ID, "event_id", item.EventID, "consumer_id", item.ConsumerID)
			} else {
				taskSynced = true
			}
			return nil
		})
		if err != nil {
			h.releaseReplayClaim(item.ID, err)
			return "", false, err
		}
		return model.DeadStatusResolved, taskSynced, nil
	}

	msg := truncateMessage(message, 2000)
	result := h.db.Model(&model.DeadLetter{}).
		Where("id = ? AND status = ?", item.ID, model.DeadStatusReplaying).
		Updates(map[string]any{
			"status":     model.DeadStatusPending,
			"last_error": msg,
		})
	if result.Error != nil {
		h.releaseReplayClaim(item.ID, result.Error)
		return "", false, result.Error
	}
	if result.RowsAffected == 0 {
		return "", false, errDeadLetterState
	}
	return model.DeadStatusPending, false, nil
}

// releaseReplayClaim 在收尾失败时尽量把死信从 replaying 放回 pending。
func (h *DeadLetterHandler) releaseReplayClaim(id uint64, cause error) {
	msg := truncateMessage("重推结果回写失败: "+cause.Error(), 2000)
	if err := h.db.Model(&model.DeadLetter{}).
		Where("id = ? AND status = ?", id, model.DeadStatusReplaying).
		Updates(map[string]any{
			"status":     model.DeadStatusPending,
			"last_error": msg,
		}).Error; err != nil {
		h.logger.Error("死信重推失败后回滚状态失败", "dead_letter_id", id, "error", err)
	}
}

func (h *DeadLetterHandler) currentConsumerState(consumerID uint64) deadLetterConsumerState {
	var state deadLetterConsumerState
	if consumerID == 0 {
		return state
	}
	var consumer model.Consumer
	if err := h.db.Unscoped().
		Select("id, name, callback_url, forward_mode, enabled, deleted_at").
		First(&consumer, consumerID).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			h.logger.Warn("读取死信当前消费端状态失败", "consumer_id", consumerID, "error", err)
		}
		return state
	}
	state.ConsumerExists = true
	state.ConsumerDeleted = consumer.DeletedAt.Valid
	state.ConsumerEnabled = consumer.Enabled
	state.CurrentConsumerName = consumer.Name
	state.CurrentCallbackURL = consumer.CallbackURL
	state.CurrentForwardMode = consumer.ForwardMode
	return state
}

func parseUseSnapshot(c *gin.Context) (bool, bool) {
	raw, exists := c.GetQuery("use_snapshot")
	if !exists || strings.TrimSpace(raw) == "" {
		return false, true
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		Fail(c, http.StatusBadRequest, "use_snapshot 必须为布尔值")
		return false, false
	}
	return value, true
}

func truncateMessage(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func toDeadLetterView(item model.DeadLetter) deadLetterView {
	return deadLetterView{
		ID:           item.ID,
		EventID:      item.EventID,
		ConsumerID:   item.ConsumerID,
		ConsumerName: item.ConsumerName,
		ForwardMode:  item.ForwardMode,
		CallbackURL:  item.CallbackURL,
		Status:       item.Status,
		RetryCount:   item.RetryCount,
		LastError:    item.LastError,
		CreatedAt:    item.CreatedAt,
		ResolvedAt:   item.ResolvedAt,
	}
}

// parseQueryTime 支持 YYYY-MM-DD 与 RFC3339 两种格式；endOfDay 时日期取当天结束。
func parseQueryTime(raw string, endOfDay bool) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, time.Local); err == nil {
		if endOfDay {
			t = t.Add(24*time.Hour - time.Second)
		}
		return &t, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t, true
	}
	return nil, false
}
