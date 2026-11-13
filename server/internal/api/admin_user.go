package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/middleware"
	"wecomrelay/internal/model"
	"wecomrelay/internal/security"
)

// AdminUserHandler 管理员账号管理（仅 admin 角色可访问）。
type AdminUserHandler struct {
	db *gorm.DB
}

func NewAdminUserHandler(db *gorm.DB) *AdminUserHandler {
	return &AdminUserHandler{db: db}
}

// List 管理员列表。
func (h *AdminUserHandler) List(c *gin.Context) {
	page, size := PageParams(c)
	query := h.db.Model(&model.AdminUser{})
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("username LIKE ? OR nickname LIKE ?", like, like)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询管理员失败")
		return
	}
	var users []model.AdminUser
	if err := query.Order("id ASC").Offset((page - 1) * size).Limit(size).Find(&users).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询管理员失败")
		return
	}
	OK(c, PageData{List: users, Total: total, Page: page, PageSize: size})
}

type createAdminRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Nickname string `json:"nickname"`
	Role     string `json:"role"`
	Enabled  *bool  `json:"enabled"`
}

// Create 新建管理员。
func (h *AdminUserHandler) Create(c *gin.Context) {
	var req createAdminRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "用户名和密码不能为空")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 3 {
		Fail(c, http.StatusBadRequest, "用户名至少需要 3 个字符")
		return
	}
	if len(req.Password) < 8 {
		Fail(c, http.StatusBadRequest, "密码至少需要 8 个字符")
		return
	}
	role, ok := normalizeRole(req.Role)
	if !ok {
		Fail(c, http.StatusBadRequest, "角色仅支持 admin 或 viewer")
		return
	}
	var exists int64
	h.db.Model(&model.AdminUser{}).Where("username = ?", req.Username).Count(&exists)
	if exists > 0 {
		Fail(c, http.StatusConflict, "用户名已存在")
		return
	}
	hash, err := security.HashPassword(req.Password)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	user := model.AdminUser{
		Username:     req.Username,
		PasswordHash: hash,
		Nickname:     strings.TrimSpace(req.Nickname),
		Role:         role,
		Enabled:      enabled,
		TokenVersion: 1,
	}
	if err := h.db.Create(&user).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "创建管理员失败")
		return
	}
	writeAudit(h.db, c, "admin_user.create", "admin_user", user.ID, "创建管理员 "+user.Username, "success")
	OK(c, user)
}

type updateAdminRequest struct {
	Nickname *string `json:"nickname"`
	Role     string  `json:"role"`
	Enabled  *bool   `json:"enabled"`
}

// Update 修改管理员资料、角色或启用状态。
func (h *AdminUserHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var user model.AdminUser
	if err := h.db.First(&user, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "管理员不存在")
		return
	}

	var req updateAdminRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	updates := map[string]any{}
	revokeTokens := false
	if req.Nickname != nil {
		updates["nickname"] = strings.TrimSpace(*req.Nickname)
	}
	if req.Role != "" {
		role, valid := normalizeRole(req.Role)
		if !valid {
			Fail(c, http.StatusBadRequest, "角色仅支持 admin 或 viewer")
			return
		}
		if id == middleware.UserID(c) && role != model.RoleAdmin {
			Fail(c, http.StatusBadRequest, "不能降低自己的角色")
			return
		}
		if role != user.Role {
			revokeTokens = true
		}
		updates["role"] = role
	}
	if req.Enabled != nil {
		if id == middleware.UserID(c) && !*req.Enabled {
			Fail(c, http.StatusBadRequest, "不能禁用自己的账号")
			return
		}
		if *req.Enabled != user.Enabled {
			revokeTokens = true
		}
		updates["enabled"] = *req.Enabled
	}
	if len(updates) == 0 {
		OK(c, user)
		return
	}
	if revokeTokens {
		updates["token_version"] = gorm.Expr("token_version + 1")
	}
	if err := h.db.Model(&user).Updates(updates).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "修改管理员失败")
		return
	}
	if err := h.db.First(&user, id).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "读取管理员失败")
		return
	}
	writeAudit(h.db, c, "admin_user.update", "admin_user", user.ID, "修改管理员 "+user.Username, "success")
	OK(c, user)
}

type resetPasswordRequest struct {
	NewPassword string `json:"new_password" binding:"required"`
}

// ResetPassword 重置管理员密码。
func (h *AdminUserHandler) ResetPassword(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req resetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.NewPassword) < 8 {
		Fail(c, http.StatusBadRequest, "新密码至少需要 8 个字符")
		return
	}
	var user model.AdminUser
	if err := h.db.First(&user, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "管理员不存在")
		return
	}
	hash, err := security.HashPassword(req.NewPassword)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := h.db.Model(&user).Updates(map[string]any{
		"password_hash": hash,
		"token_version": gorm.Expr("token_version + 1"),
	}).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "重置密码失败")
		return
	}
	writeAudit(h.db, c, "admin_user.reset_password", "admin_user", user.ID, "重置管理员密码 "+user.Username, "success")
	OK(c, gin.H{"message": "密码已重置"})
}

// Delete 删除管理员（不能删除自己，且必须保留至少一个启用的管理员）。
func (h *AdminUserHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if id == middleware.UserID(c) {
		Fail(c, http.StatusBadRequest, "不能删除自己的账号")
		return
	}
	var user model.AdminUser
	if err := h.db.First(&user, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "管理员不存在")
		return
	}
	if user.Role == model.RoleAdmin {
		var admins int64
		h.db.Model(&model.AdminUser{}).Where("role = ? AND enabled = ?", model.RoleAdmin, true).Count(&admins)
		if admins <= 1 {
			Fail(c, http.StatusBadRequest, "必须保留至少一个启用的管理员")
			return
		}
	}
	if err := h.db.Delete(&user).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "删除管理员失败")
		return
	}
	writeAudit(h.db, c, "admin_user.delete", "admin_user", user.ID, "删除管理员 "+user.Username, "success")
	OK(c, gin.H{"message": "已删除"})
}

func normalizeRole(role string) (string, bool) {
	switch strings.TrimSpace(role) {
	case "", model.RoleAdmin:
		return model.RoleAdmin, true
	case model.RoleViewer:
		return model.RoleViewer, true
	default:
		return "", false
	}
}
