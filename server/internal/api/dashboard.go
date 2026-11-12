package api

import (
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/model"
)

// DashboardHandler 首页概览。
type DashboardHandler struct {
	db *gorm.DB
}

func NewDashboardHandler(db *gorm.DB) *DashboardHandler {
	return &DashboardHandler{db: db}
}

// Summary 返回消费端、任务与死信统计。
func (h *DashboardHandler) Summary(c *gin.Context) {
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	var consumerTotal, consumerEnabled int64
	h.db.Model(&model.Consumer{}).Count(&consumerTotal)
	h.db.Model(&model.Consumer{}).Where("enabled = ?", true).Count(&consumerEnabled)

	var taskPending, taskSuccessToday, taskDeadToday int64
	h.db.Model(&model.RelayTask{}).Where("status = ?", model.TaskStatusPending).Count(&taskPending)
	h.db.Model(&model.RelayTask{}).Where("status = ? AND updated_at >= ?", model.TaskStatusSuccess, startOfDay).Count(&taskSuccessToday)
	h.db.Model(&model.RelayTask{}).Where("status = ? AND updated_at >= ?", model.TaskStatusDead, startOfDay).Count(&taskDeadToday)

	var deadPending, deadReplaying int64
	h.db.Model(&model.DeadLetter{}).Where("status = ?", model.DeadStatusPending).Count(&deadPending)
	h.db.Model(&model.DeadLetter{}).Where("status = ?", model.DeadStatusReplaying).Count(&deadReplaying)

	// 快速开始引导状态：是否收到过入站事件、是否已有成功投递。
	var taskTotal, taskSuccess int64
	h.db.Model(&model.RelayTask{}).Count(&taskTotal)
	h.db.Model(&model.RelayTask{}).Where("status = ?", model.TaskStatusSuccess).Count(&taskSuccess)

	OK(c, gin.H{
		"consumers": gin.H{
			"total":   consumerTotal,
			"enabled": consumerEnabled,
		},
		"tasks": gin.H{
			"pending":       taskPending,
			"success_today": taskSuccessToday,
			"dead_today":    taskDeadToday,
		},
		"dead_letters": gin.H{
			"pending":   deadPending,
			"replaying": deadReplaying,
		},
		"quick_start": gin.H{
			"has_inbound": taskTotal > 0,
			"has_success": taskSuccess > 0,
		},
		"server_time": now,
	})
}
