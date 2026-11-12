package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/model"
)

// AuditHandler 操作审计查询。
type AuditHandler struct {
	db *gorm.DB
}

func NewAuditHandler(db *gorm.DB) *AuditHandler {
	return &AuditHandler{db: db}
}

// List 审计日志列表。
func (h *AuditHandler) List(c *gin.Context) {
	page, size := PageParams(c)
	query := h.db.Model(&model.AuditLog{})
	if action := strings.TrimSpace(c.Query("action")); action != "" {
		query = query.Where("action = ?", action)
	}
	if adminName := strings.TrimSpace(c.Query("admin_name")); adminName != "" {
		query = query.Where("admin_name LIKE ?", "%"+adminName+"%")
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("action LIKE ? OR detail LIKE ?", like, like)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询审计日志失败")
		return
	}
	var logs []model.AuditLog
	if err := query.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&logs).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询审计日志失败")
		return
	}
	OK(c, PageData{List: logs, Total: total, Page: page, PageSize: size})
}
