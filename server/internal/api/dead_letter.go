package api

import (
	"context"
	"errors"
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
	errDeadLetterNotFound = errors.New("死信不存在")
	errDeadLetterBusy     = errors.New("死信正在重推，请勿重复操作")
	errDeadLetterState    = errors.New("当前状态不允许该操作")
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

type deadLetterDetail struct {
	deadLetterView
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
		deadLetterView:      toDeadLetterView(item),
		RawXML:              item.RawXML,
		RawEncryptedBody:    item.RawEncryptedBody,
		InboundQuery:        item.InboundQuery,
		JSONPayload:         item.JSONPayload,
		AuthType:            item.AuthType,
		WecomTokenMasked:    security.MaskSecret(wecomToken),
		WecomAESKeyMasked:   security.MaskSecret(aesKey),
		WecomKeyFingerprint: security.KeyFingerprint(wecomToken, aesKey),
		WecomCorpID:         item.WecomCorpID,
		WecomAgentID:        item.AgentID,
		TimeoutMS:           item.TimeoutMS,
	})
}

// Replay 单条重推。
func (h *DeadLetterHandler) Replay(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	status, err := h.replayOne(context.Background(), id)
	switch {
	case errors.Is(err, errDeadLetterNotFound):
		Fail(c, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, errDeadLetterBusy), errors.Is(err, errDeadLetterState):
		Fail(c, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeAudit(h.db, c, "dead_letter.replay", "dead_letter", id, "单条重推失败", "failed")
		Fail(c, http.StatusBadGateway, "重推失败: "+err.Error())
		return
	}
	writeAudit(h.db, c, "dead_letter.replay", "dead_letter", id, "单条重推成功", "success")
	OK(c, gin.H{"status": status})
}

type batchReplayRequest struct {
	IDs []uint64 `json:"ids"`
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
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}

	type replayResult struct {
		id     uint64
		status string
		err    error
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
			status, err := h.replayOne(context.Background(), id)
			resultCh <- replayResult{id: id, status: status, err: err}
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
		results = append(results, gin.H{"id": item.id, "success": true, "status": item.status})
	}
	writeAudit(h.db, c, "dead_letter.replay_batch", "dead_letter", 0,
		"批量重推，成功 "+strconv.Itoa(successCount)+" 条，失败 "+strconv.Itoa(failedCount)+" 条", "success")
	OK(c, gin.H{
		"total":   len(seen),
		"success": successCount,
		"failed":  failedCount,
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

// replayOne 执行单条死信重推，返回最终状态。
func (h *DeadLetterHandler) replayOne(ctx context.Context, id uint64) (string, error) {
	claim := h.db.Model(&model.DeadLetter{}).
		Where("id = ? AND status IN ?", id, []string{model.DeadStatusPending, model.DeadStatusDiscarded}).
		Update("status", model.DeadStatusReplaying)
	if claim.Error != nil {
		return "", claim.Error
	}
	if claim.RowsAffected == 0 {
		var current model.DeadLetter
		if err := h.db.First(&current, id).Error; err != nil {
			return "", errDeadLetterNotFound
		}
		if current.Status == model.DeadStatusReplaying {
			return "", errDeadLetterBusy
		}
		return "", errDeadLetterState
	}

	var item model.DeadLetter
	if err := h.db.First(&item, id).Error; err != nil {
		return "", errDeadLetterNotFound
	}

	target := relay.Target{
		ConsumerID:  item.ConsumerID,
		Name:        item.ConsumerName,
		CallbackURL: item.CallbackURL,
		ForwardMode: item.ForwardMode,
		TimeoutMS:   item.TimeoutMS,
	}

	var elapsed time.Duration
	var deliverErr error

	if item.ForwardMode == model.ForwardModeWecomProxy {
		if item.RawEncryptedBody == "" || item.InboundQuery == "" {
			return h.finishReplay(item, false, "该死信缺少原始加密报文快照，无法原样透传")
		}
		_, elapsed, deliverErr = h.sender.DeliverProxy(ctx, target, item.RawEncryptedBody, item.InboundQuery)
	} else {
		authConfig, err := h.cipher.Decrypt(item.AuthConfig)
		if err != nil {
			return h.finishReplay(item, false, "认证配置解密失败: "+err.Error())
		}
		wecomToken, err := h.cipher.Decrypt(item.WecomTokenSnapshot)
		if err != nil {
			return h.finishReplay(item, false, "Token 解密失败: "+err.Error())
		}
		wecomAESKey, err := h.cipher.Decrypt(item.WecomAESKeySnapshot)
		if err != nil {
			return h.finishReplay(item, false, "EncodingAESKey 解密失败: "+err.Error())
		}
		target.AuthType = item.AuthType
		target.AuthConfig = authConfig
		target.WecomToken = wecomToken
		target.WecomEncodingAESKey = wecomAESKey
		target.WecomCorpID = item.WecomCorpID

		payload := relay.Payload{
			EventID:     item.EventID,
			AgentID:     item.AgentID,
			RawXML:      item.RawXML,
			JSONPayload: item.JSONPayload,
		}
		_, elapsed, deliverErr = h.sender.Deliver(ctx, target, payload)
	}
	if deliverErr != nil {
		return h.finishReplay(item, false, deliverErr.Error())
	}
	h.logger.Info("死信重推成功", "dead_letter_id", item.ID, "event_id", item.EventID,
		"consumer_id", item.ConsumerID, "latency_ms", elapsed.Milliseconds())
	return h.finishReplay(item, true, "")
}

func (h *DeadLetterHandler) finishReplay(item model.DeadLetter, success bool, message string) (string, error) {
	if success {
		now := time.Now()
		err := h.db.Model(&model.DeadLetter{}).
			Where("id = ? AND status = ?", item.ID, model.DeadStatusReplaying).
			Updates(map[string]any{
				"status":      model.DeadStatusResolved,
				"resolved_at": now,
				"last_error":  "",
			}).Error
		if err != nil {
			return "", err
		}
		return model.DeadStatusResolved, nil
	}

	if len(message) > 2000 {
		message = message[:2000]
	}
	err := h.db.Model(&model.DeadLetter{}).
		Where("id = ? AND status = ?", item.ID, model.DeadStatusReplaying).
		Updates(map[string]any{
			"status":     model.DeadStatusPending,
			"last_error": message,
		}).Error
	if err != nil {
		return "", err
	}
	return model.DeadStatusPending, errors.New(message)
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
