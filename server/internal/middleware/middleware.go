package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/gin-gonic/gin"

	"wecomrelay/internal/security"
)

const (
	ctxUserID   = "auth_user_id"
	ctxUsername = "auth_username"
	ctxRole     = "auth_role"
)

// RequestLogger 输出结构化访问日志；不记录 Query，避免签名/echostr 等进入日志。
func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Info("http_request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}

// Recovery 捕获 panic 并记录结构化日志。
func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, recovered any) {
		logger.Error("http_panic", "path", c.Request.URL.Path, "error", recovered)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": 500, "msg": "服务器内部错误"})
	})
}

// CORS 允许配置的来源跨域访问管理 API。
func CORS(origins []string) gin.HandlerFunc {
	allowAll := false
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if origin == "*" {
			allowAll = true
		}
		allowed[origin] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			if allowAll {
				c.Header("Access-Control-Allow-Origin", "*")
			} else if _, ok := allowed[origin]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
			}
		}
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,X-Requested-With")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// JWTAuth 校验管理端令牌并写入操作人上下文。
func JWTAuth(manager *security.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": "未登录或令牌缺失"})
			return
		}
		claims, err := manager.Parse(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": err.Error()})
			return
		}
		c.Set(ctxUserID, claims.UserID)
		c.Set(ctxUsername, claims.Username)
		c.Set(ctxRole, claims.Role)
		c.Next()
	}
}

// CasbinAuth 按角色执行接口级授权。
func CasbinAuth(enforcer *casbin.Enforcer) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := Role(c)
		ok, err := enforcer.Enforce(role, c.FullPath(), c.Request.Method)
		if err != nil || !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403, "msg": "无权访问该接口"})
			return
		}
		c.Next()
	}
}

// RequireRole 限制仅指定角色可访问。
func RequireRole(roles ...string) gin.HandlerFunc {
	set := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		set[role] = struct{}{}
	}
	return func(c *gin.Context) {
		if _, ok := set[Role(c)]; !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403, "msg": "需要更高的权限"})
			return
		}
		c.Next()
	}
}

// UserID 返回当前管理员 ID。
func UserID(c *gin.Context) uint64 {
	if v, ok := c.Get(ctxUserID); ok {
		if id, ok := v.(uint64); ok {
			return id
		}
	}
	return 0
}

// Username 返回当前管理员用户名。
func Username(c *gin.Context) string {
	if v, ok := c.Get(ctxUsername); ok {
		if name, ok := v.(string); ok {
			return name
		}
	}
	return ""
}

// Role 返回当前管理员角色。
func Role(c *gin.Context) string {
	if v, ok := c.Get(ctxRole); ok {
		if role, ok := v.(string); ok {
			return role
		}
	}
	return ""
}
