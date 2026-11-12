package router

import (
	"log/slog"
	"net/http"

	"github.com/casbin/casbin/v2"
	casbinmodel "github.com/casbin/casbin/v2/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/api"
	"wecomrelay/internal/config"
	"wecomrelay/internal/middleware"
	appmodel "wecomrelay/internal/model"
	"wecomrelay/internal/relay"
	"wecomrelay/internal/security"
)

const casbinModelText = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.sub == p.sub && keyMatch(r.obj, p.obj) && regexMatch(r.act, p.act)
`

// Dependencies 路由所需的依赖。
type Dependencies struct {
	Config *config.Config
	DB     *gorm.DB
	Cipher *security.Cipher
	JWT    *security.Manager
	Sender *relay.Sender
	Logger *slog.Logger
}

// New 构建 Gin 引擎并注册全部路由。
func New(deps Dependencies) (*gin.Engine, error) {
	enforcer, err := buildEnforcer()
	if err != nil {
		return nil, err
	}

	authHandler := api.NewAuthHandler(deps.DB, deps.JWT, deps.Logger)
	adminHandler := api.NewAdminUserHandler(deps.DB)
	consumerHandler := api.NewConsumerHandler(deps.DB, deps.Cipher, deps.Sender, deps.Logger)
	wecomConfigHandler := api.NewWecomConfigHandler(deps.DB, deps.Cipher, deps.Logger)
	deadLetterHandler := api.NewDeadLetterHandler(deps.DB, deps.Cipher, deps.Sender, deps.Logger)
	dashboardHandler := api.NewDashboardHandler(deps.DB)
	auditHandler := api.NewAuditHandler(deps.DB)
	callbackHandler := api.NewCallbackHandler(deps.DB, deps.Cipher, deps.Logger, deps.Config.CallbackMaxSkew)

	engine := gin.New()
	engine.Use(middleware.Recovery(deps.Logger), middleware.RequestLogger(deps.Logger), middleware.CORS(deps.Config.CORSOrigins))
	_ = engine.SetTrustedProxies(nil)

	engine.GET("/healthz", func(c *gin.Context) {
		sqlDB, err := deps.DB.DB()
		if err != nil || sqlDB.Ping() != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 企微协议入口：不走 JWT，走企微验签。
	engine.GET("/wecom/callback", callbackHandler.Verify)
	engine.POST("/wecom/callback", callbackHandler.Receive)

	apiGroup := engine.Group("/api")
	apiGroup.POST("/base/login", authHandler.Login)

	secured := apiGroup.Group("")
	secured.Use(middleware.JWTAuth(deps.JWT), middleware.CasbinAuth(enforcer))
	{
		base := secured.Group("/base")
		base.GET("/user-info", authHandler.UserInfo)
		base.POST("/logout", authHandler.Logout)
		base.POST("/change-password", authHandler.ChangePassword)

		secured.GET("/dashboard/summary", dashboardHandler.Summary)

		consumers := secured.Group("/consumers")
		consumers.GET("", consumerHandler.List)
		consumers.POST("", consumerHandler.Create)
		consumers.GET("/:id", consumerHandler.Get)
		consumers.PUT("/:id", consumerHandler.Update)
		consumers.DELETE("/:id", consumerHandler.Delete)
		consumers.POST("/:id/test", consumerHandler.Test)

		wecomConfig := secured.Group("/wecom-config")
		wecomConfig.GET("", wecomConfigHandler.List)
		wecomConfig.POST("", wecomConfigHandler.Create)
		wecomConfig.PUT("/:id", wecomConfigHandler.Update)
		wecomConfig.DELETE("/:id", wecomConfigHandler.Delete)

		deadLetters := secured.Group("/dead-letters")
		deadLetters.GET("", deadLetterHandler.List)
		deadLetters.POST("/replay-batch", deadLetterHandler.ReplayBatch)
		deadLetters.POST("/batch-delete", deadLetterHandler.DeleteBatch)
		deadLetters.GET("/:id", deadLetterHandler.Get)
		deadLetters.POST("/:id/replay", deadLetterHandler.Replay)
		deadLetters.PUT("/:id/discard", deadLetterHandler.Discard)
		deadLetters.DELETE("/:id", deadLetterHandler.Delete)

		auditLogs := secured.Group("/audit-logs")
		auditLogs.GET("", auditHandler.List)

		// 管理员账号管理仅 admin 可访问（Casbin 之外再加一层角色限制）。
		adminUsers := secured.Group("/admin-users", middleware.RequireRole(appmodel.RoleAdmin))
		adminUsers.GET("", adminHandler.List)
		adminUsers.POST("", adminHandler.Create)
		adminUsers.PUT("/:id", adminHandler.Update)
		adminUsers.DELETE("/:id", adminHandler.Delete)
		adminUsers.PUT("/:id/reset-password", adminHandler.ResetPassword)
	}

	return engine, nil
}

func buildEnforcer() (*casbin.Enforcer, error) {
	m, err := casbinmodel.NewModelFromString(casbinModelText)
	if err != nil {
		return nil, err
	}
	enforcer, err := casbin.NewEnforcer(m)
	if err != nil {
		return nil, err
	}
	if _, err := enforcer.AddPolicy(appmodel.RoleAdmin, "/api/*", "(GET)|(POST)|(PUT)|(DELETE)"); err != nil {
		return nil, err
	}
	if _, err := enforcer.AddPolicy(appmodel.RoleViewer, "/api/*", "GET"); err != nil {
		return nil, err
	}
	// 只读账号也允许退出登录与修改自己的密码。
	if _, err := enforcer.AddPolicy(appmodel.RoleViewer, "/api/base/logout", "POST"); err != nil {
		return nil, err
	}
	if _, err := enforcer.AddPolicy(appmodel.RoleViewer, "/api/base/change-password", "POST"); err != nil {
		return nil, err
	}
	return enforcer, nil
}
