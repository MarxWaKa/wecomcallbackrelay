package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/middleware"
	"wecomrelay/internal/model"
	"wecomrelay/internal/security"
)

// AuthHandler 登录与账号自助接口。
type AuthHandler struct {
	db     *gorm.DB
	jwt    *security.Manager
	logger *slog.Logger
}

func NewAuthHandler(db *gorm.DB, jwt *security.Manager, logger *slog.Logger) *AuthHandler {
	return &AuthHandler{db: db, jwt: jwt, logger: logger}
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login 管理员登录。
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "用户名和密码不能为空")
		return
	}
	req.Username = strings.TrimSpace(req.Username)

	var user model.AdminUser
	if err := h.db.Where("username = ?", req.Username).First(&user).Error; err != nil {
		Fail(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if !user.Enabled {
		Fail(c, http.StatusForbidden, "账号已被禁用")
		return
	}
	if !security.CheckPassword(user.PasswordHash, req.Password) {
		Fail(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}

	token, expiresAt, err := h.jwt.Generate(user)
	if err != nil {
		h.logger.Error("签发登录令牌失败", "error", err)
		Fail(c, http.StatusInternalServerError, "登录失败")
		return
	}
	now := time.Now()
	h.db.Model(&user).Update("last_login_at", now)
	user.LastLoginAt = &now

	OK(c, gin.H{
		"token":      token,
		"expires_at": expiresAt,
		"user":       user,
	})
}

// UserInfo 返回当前登录管理员信息。
func (h *AuthHandler) UserInfo(c *gin.Context) {
	var user model.AdminUser
	if err := h.db.First(&user, middleware.UserID(c)).Error; err != nil {
		Fail(c, http.StatusUnauthorized, "账号不存在")
		return
	}
	if !user.Enabled {
		Fail(c, http.StatusForbidden, "账号已被禁用")
		return
	}
	OK(c, user)
}

// Logout 登出（JWT 无状态，客户端丢弃令牌即可）。
func (h *AuthHandler) Logout(c *gin.Context) {
	OK(c, gin.H{"message": "已退出登录"})
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// ChangePassword 修改当前账号密码。
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "请填写原密码与新密码")
		return
	}
	if len(req.NewPassword) < 8 {
		Fail(c, http.StatusBadRequest, "新密码至少需要 8 个字符")
		return
	}
	var user model.AdminUser
	if err := h.db.First(&user, middleware.UserID(c)).Error; err != nil {
		Fail(c, http.StatusUnauthorized, "账号不存在")
		return
	}
	if !security.CheckPassword(user.PasswordHash, req.OldPassword) {
		Fail(c, http.StatusBadRequest, "原密码错误")
		return
	}
	hash, err := security.HashPassword(req.NewPassword)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := h.db.Model(&user).Update("password_hash", hash).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "修改密码失败")
		return
	}
	writeAudit(h.db, c, "auth.change_password", "admin_user", user.ID, "修改本人密码", "success")
	OK(c, gin.H{"message": "密码已修改，请重新登录"})
}
