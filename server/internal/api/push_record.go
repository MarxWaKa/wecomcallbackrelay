package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/model"
)

// 推送记录只允许删除已结束的任务，待投递/投递中的任务删除会导致丢投递。
var deletableTaskStatuses = []string{
	model.TaskStatusSuccess,
	model.TaskStatusFailed,
	model.TaskStatusDead,
}

// PushRecordHandler 推送记录查询与历史清理。
type PushRecordHandler struct {
	db     *gorm.DB
	logger *slog.Logger
}

func NewPushRecordHandler(db *gorm.DB, logger *slog.Logger) *PushRecordHandler {
	return &PushRecordHandler{db: db, logger: logger}
}

type pushRecordView struct {
	ID             uint64     `json:"id"`
	EventID        string     `json:"event_id"`
	ConsumerID     uint64     `json:"consumer_id"`
	ConsumerName   string     `json:"consumer_name"`
	CallbackURL    string     `json:"callback_url"`
	ForwardMode    string     `json:"forward_mode"`
	AgentID        string     `json:"agent_id"`
	Status         string     `json:"status"`
	RetryCount     int        `json:"retry_count"`
	MaxRetry       int        `json:"max_retry"`
	NextRetryAt    *time.Time `json:"next_retry_at"`
	LastHTTPStatus int        `json:"last_http_status"`
	LastLatencyMS  int64      `json:"last_latency_ms"`
	LastError      string     `json:"last_error"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type pushRecordDetail struct {
	pushRecordView
	RawXML           string `json:"raw_xml"`
	RawEncryptedBody string `json:"raw_encrypted_body"`
	InboundQuery     string `json:"inbound_query"`
	JSONPayload      string `json:"json_payload"`
}

// List 推送记录列表（不返回大字段）。
func (h *PushRecordHandler) List(c *gin.Context) {
	page, size := PageParams(c)
	query := h.db.Model(&model.RelayTask{})

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
		Fail(c, http.StatusInternalServerError, "查询推送记录失败")
		return
	}
	var items []model.RelayTask
	if err := query.
		Select("id, event_id, consumer_id, consumer_name, callback_url, forward_mode, agent_id, status, retry_count, max_retry, next_retry_at, last_http_status, last_latency_ms, last_error, created_at, updated_at").
		Order("id DESC").
		Offset((page - 1) * size).
		Limit(size).
		Find(&items).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询推送记录失败")
		return
	}
	h.fillConsumerSnapshots(items)

	views := make([]pushRecordView, 0, len(items))
	for _, item := range items {
		views = append(views, toPushRecordView(item))
	}
	OK(c, PageData{List: views, Total: total, Page: page, PageSize: size})
}

// Get 推送记录详情。
func (h *PushRecordHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.RelayTask
	if err := h.db.First(&item, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "推送记录不存在")
		return
	}
	items := []model.RelayTask{item}
	h.fillConsumerSnapshots(items)
	item = items[0]

	OK(c, pushRecordDetail{
		pushRecordView:   toPushRecordView(item),
		RawXML:           item.RawXML,
		RawEncryptedBody: item.RawEncryptedBody,
		InboundQuery:     item.InboundQuery,
		JSONPayload:      item.JSONPayload,
	})
}

// Delete 永久删除单条推送记录，仅允许删除已结束的任务。
func (h *PushRecordHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.RelayTask
	if err := h.db.First(&item, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "推送记录不存在")
		return
	}
	if !isDeletableTaskStatus(item.Status) {
		Fail(c, http.StatusConflict, "待投递/投递中的任务不可删除，请等待投递结束")
		return
	}
	// 条件删除避免“检查后被 Worker 抢占”的竞态。
	result := h.db.Where("id = ? AND status IN ?", id, deletableTaskStatuses).Delete(&model.RelayTask{})
	if result.Error != nil {
		Fail(c, http.StatusInternalServerError, "删除推送记录失败")
		return
	}
	if result.RowsAffected == 0 {
		var current model.RelayTask
		if err := h.db.First(&current, id).Error; err != nil {
			Fail(c, http.StatusNotFound, "推送记录不存在")
			return
		}
		Fail(c, http.StatusConflict, "任务状态已变化，请刷新后重试")
		return
	}
	writeAudit(h.db, c, "push_record.delete", "push_record", item.ID, "删除推送记录 "+item.EventID, "success")
	OK(c, gin.H{"message": "已删除"})
}

type pushRecordBatchDeleteRequest struct {
	IDs []uint64 `json:"ids"`
}

// DeleteBatch 批量永久删除推送记录，单次上限 100 条。
func (h *PushRecordHandler) DeleteBatch(c *gin.Context) {
	var req pushRecordBatchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	if len(req.IDs) == 0 {
		Fail(c, http.StatusBadRequest, "请选择要删除的推送记录")
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
		Fail(c, http.StatusBadRequest, "请选择要删除的推送记录")
		return
	}

	result := h.db.Where("id IN ? AND status IN ?", ids, deletableTaskStatuses).
		Delete(&model.RelayTask{})
	if result.Error != nil {
		Fail(c, http.StatusInternalServerError, "批量删除推送记录失败")
		return
	}

	// 删除后统计仍未结束的任务，作为批量删除的跳过项。
	skippedIDs := make([]uint64, 0)
	if err := h.db.Model(&model.RelayTask{}).
		Where("id IN ? AND status IN ?", ids, []string{model.TaskStatusPending, model.TaskStatusSending}).
		Pluck("id", &skippedIDs).Error; err != nil {
		h.logger.Warn("统计未删除的推送记录失败", "error", err)
	}

	skipped := int64(len(ids)) - result.RowsAffected
	writeAudit(h.db, c, "push_record.delete_batch", "push_record", 0,
		"批量删除推送记录，共 "+strconv.Itoa(len(ids))+" 条，删除 "+strconv.FormatInt(result.RowsAffected, 10)+" 条", "success")
	OK(c, gin.H{
		"total":       len(ids),
		"deleted":     result.RowsAffected,
		"skipped":     skipped,
		"skipped_ids": skippedIDs,
	})
}

// fillConsumerSnapshots 为老数据补齐消费端名称与回调地址；已有快照不覆盖。
func (h *PushRecordHandler) fillConsumerSnapshots(items []model.RelayTask) {
	ids := make([]uint64, 0)
	seen := make(map[uint64]struct{})
	for _, item := range items {
		if item.ConsumerID == 0 || (item.ConsumerName != "" && item.CallbackURL != "") {
			continue
		}
		if _, ok := seen[item.ConsumerID]; ok {
			continue
		}
		seen[item.ConsumerID] = struct{}{}
		ids = append(ids, item.ConsumerID)
	}
	if len(ids) == 0 {
		return
	}

	var consumers []model.Consumer
	if err := h.db.Unscoped().
		Select("id, name, callback_url").
		Where("id IN ?", ids).
		Find(&consumers).Error; err != nil {
		h.logger.Warn("回查推送记录消费端快照失败", "error", err)
		return
	}
	byID := make(map[uint64]model.Consumer, len(consumers))
	for _, consumer := range consumers {
		byID[consumer.ID] = consumer
	}
	for i := range items {
		consumer, ok := byID[items[i].ConsumerID]
		if !ok {
			continue
		}
		if items[i].ConsumerName == "" {
			items[i].ConsumerName = consumer.Name
		}
		if items[i].CallbackURL == "" {
			items[i].CallbackURL = consumer.CallbackURL
		}
	}
}

func isDeletableTaskStatus(status string) bool {
	for _, item := range deletableTaskStatuses {
		if status == item {
			return true
		}
	}
	return false
}

func toPushRecordView(item model.RelayTask) pushRecordView {
	return pushRecordView{
		ID:             item.ID,
		EventID:        item.EventID,
		ConsumerID:     item.ConsumerID,
		ConsumerName:   item.ConsumerName,
		CallbackURL:    item.CallbackURL,
		ForwardMode:    item.ForwardMode,
		AgentID:        item.AgentID,
		Status:         item.Status,
		RetryCount:     item.RetryCount,
		MaxRetry:       item.MaxRetry,
		NextRetryAt:    item.NextRetryAt,
		LastHTTPStatus: item.LastHTTPStatus,
		LastLatencyMS:  item.LastLatencyMS,
		LastError:      item.LastError,
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
	}
}
