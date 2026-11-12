package api

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/middleware"
	"wecomrelay/internal/model"
)

// Response 管理 API 统一响应结构。
type Response struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

// OK 返回成功响应。
func OK(c *gin.Context, data any) {
	c.JSON(200, Response{Code: 0, Msg: "ok", Data: data})
}

// Fail 返回错误响应。
func Fail(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, Response{Code: status, Msg: msg})
}

// PageData 分页数据。
type PageData struct {
	List     any   `json:"list"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

// PageParams 解析分页参数，默认第 1 页、每页 20 条，最大 100 条。
func PageParams(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

// writeAudit 记录管理端关键操作；失败不影响主流程。
func writeAudit(db *gorm.DB, c *gin.Context, action, targetType string, targetID uint64, detail, result string) {
	_ = db.Create(&model.AuditLog{
		AdminID:    middleware.UserID(c),
		AdminName:  middleware.Username(c),
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
		Result:     result,
	}).Error
}
