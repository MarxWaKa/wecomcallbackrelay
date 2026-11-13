package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"wecomrelay/internal/config"
	"wecomrelay/internal/model"
	"wecomrelay/internal/relay"
	"wecomrelay/internal/security"
)

type routeFixture struct {
	engine *gin.Engine
	db     *gorm.DB
}

func newRouteFixture(t *testing.T) *routeFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", url.QueryEscape("router-"+t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.AdminUser{},
		&model.Consumer{},
		&model.WecomConfig{},
		&model.RelayTask{},
		&model.DeadLetter{},
		&model.AuditLog{},
	); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}

	cipher, err := security.NewCipher("router-test-encrypt-key-123456")
	if err != nil {
		t.Fatalf("创建加密器失败: %v", err)
	}
	jwtManager := security.NewJWTManager("router-test-jwt-secret-1234567890", time.Hour)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	engine, err := New(Dependencies{
		Config: &config.Config{
			CORSOrigins:     []string{"*"},
			CallbackMaxSkew: 300 * time.Second,
		},
		DB:     db,
		Cipher: cipher,
		JWT:    jwtManager,
		Sender: relay.NewSender(logger, false),
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("初始化路由失败: %v", err)
	}
	return &routeFixture{engine: engine, db: db}
}

func createUser(t *testing.T, db *gorm.DB, username, password, role string) {
	t.Helper()
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	if err := db.Create(&model.AdminUser{
		Username:     username,
		PasswordHash: hash,
		Nickname:     username,
		Role:         role,
		Enabled:      true,
	}).Error; err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}
}

func (f *routeFixture) userID(t *testing.T, username string) uint64 {
	t.Helper()
	var user model.AdminUser
	if err := f.db.Where("username = ?", username).First(&user).Error; err != nil {
		t.Fatalf("查询测试管理员失败: %v", err)
	}
	return user.ID
}

func (f *routeFixture) request(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func (f *routeFixture) login(t *testing.T, username, password string) (string, bool) {
	t.Helper()
	rec := f.request(t, http.MethodPost, "/api/base/login", "", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != http.StatusOK {
		return "", false
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析登录响应失败: %v", err)
	}
	return payload.Data.Token, payload.Code == 0
}

func TestHealthAndUnauthorized(t *testing.T) {
	f := newRouteFixture(t)

	health := f.request(t, http.MethodGet, "/healthz", "", nil)
	if health.Code != http.StatusOK {
		t.Fatalf("healthz 状态码 %d", health.Code)
	}
	unauthorized := f.request(t, http.MethodGet, "/api/dashboard/summary", "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("未携带令牌应返回 401，实际 %d", unauthorized.Code)
	}
}

func TestLoginAndRBAC(t *testing.T) {
	f := newRouteFixture(t)
	createUser(t, f.db, "admin1", "Admin@123456", model.RoleAdmin)
	createUser(t, f.db, "viewer1", "Viewer@123456", model.RoleViewer)

	adminToken, ok := f.login(t, "admin1", "Admin@123456")
	if !ok || adminToken == "" {
		t.Fatal("管理员登录失败")
	}
	viewerToken, ok := f.login(t, "viewer1", "Viewer@123456")
	if !ok || viewerToken == "" {
		t.Fatal("只读账号登录失败")
	}

	// 错误密码
	if _, ok := f.login(t, "admin1", "wrong-password"); ok {
		t.Fatal("错误密码不应登录成功")
	}

	// admin 可以新增消费端
	consumerBody := map[string]any{
		"name":         "测试消费端",
		"callback_url": "https://consumer.example.com/callback",
		"forward_mode": "json",
		"auth_type":    "none",
		"enabled":      true,
		"timeout_ms":   5000,
		"max_retry":    5,
	}
	created := f.request(t, http.MethodPost, "/api/consumers", adminToken, consumerBody)
	if created.Code != http.StatusOK {
		t.Fatalf("admin 新增消费端应成功，实际 %d: %s", created.Code, created.Body.String())
	}

	// viewer 只能 GET
	viewerGet := f.request(t, http.MethodGet, "/api/consumers", viewerToken, nil)
	if viewerGet.Code != http.StatusOK {
		t.Fatalf("viewer GET 应允许，实际 %d", viewerGet.Code)
	}
	viewerPost := f.request(t, http.MethodPost, "/api/consumers", viewerToken, consumerBody)
	if viewerPost.Code != http.StatusForbidden {
		t.Fatalf("viewer POST 应返回 403，实际 %d", viewerPost.Code)
	}

	// viewer 不能访问管理员管理接口（即使 GET）
	viewerAdmin := f.request(t, http.MethodGet, "/api/admin-users", viewerToken, nil)
	if viewerAdmin.Code != http.StatusForbidden {
		t.Fatalf("viewer 访问管理员接口应返回 403，实际 %d", viewerAdmin.Code)
	}
	adminUsers := f.request(t, http.MethodGet, "/api/admin-users", adminToken, nil)
	if adminUsers.Code != http.StatusOK {
		t.Fatalf("admin 访问管理员接口应成功，实际 %d", adminUsers.Code)
	}

	// viewer 必须能退出登录与修改密码；错误原密码应返回 400 而不是 403。
	viewerLogout := f.request(t, http.MethodPost, "/api/base/logout", viewerToken, nil)
	if viewerLogout.Code != http.StatusOK {
		t.Fatalf("viewer 登出应成功，实际 %d", viewerLogout.Code)
	}
	// 登出会使旧令牌立即失效。
	viewerAfterLogout := f.request(t, http.MethodGet, "/api/consumers", viewerToken, nil)
	if viewerAfterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("登出后旧令牌应返回 401，实际 %d", viewerAfterLogout.Code)
	}
	viewerToken, ok = f.login(t, "viewer1", "Viewer@123456")
	if !ok || viewerToken == "" {
		t.Fatal("viewer 重新登录失败")
	}
	viewerChange := f.request(t, http.MethodPost, "/api/base/change-password", viewerToken, map[string]string{
		"old_password": "wrong-old-password",
		"new_password": "Viewer@1234567",
	})
	if viewerChange.Code != http.StatusBadRequest {
		t.Fatalf("viewer 修改密码应通过权限校验并因原密码错误返回 400，实际 %d", viewerChange.Code)
	}

	// 死信批量重推路由存在（空 ids 应返回 400 而不是 404）
	batch := f.request(t, http.MethodPost, "/api/dead-letters/replay-batch", adminToken, map[string]any{"ids": []uint64{}})
	if batch.Code != http.StatusBadRequest {
		t.Fatalf("空批量重推应返回 400，实际 %d", batch.Code)
	}
}

func TestJWTAuthRevokedAfterLogoutAndPasswordChange(t *testing.T) {
	f := newRouteFixture(t)
	createUser(t, f.db, "admin1", "Admin@123456", model.RoleAdmin)

	token, ok := f.login(t, "admin1", "Admin@123456")
	if !ok || token == "" {
		t.Fatal("管理员登录失败")
	}
	if rec := f.request(t, http.MethodGet, "/api/dashboard/summary", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("登录后访问应成功，实际 %d", rec.Code)
	}

	if rec := f.request(t, http.MethodPost, "/api/base/logout", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("登出失败: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.request(t, http.MethodGet, "/api/dashboard/summary", token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("登出后旧令牌应返回 401，实际 %d", rec.Code)
	}

	token, ok = f.login(t, "admin1", "Admin@123456")
	if !ok || token == "" {
		t.Fatal("登出后重新登录失败")
	}
	change := f.request(t, http.MethodPost, "/api/base/change-password", token, map[string]string{
		"old_password": "Admin@123456",
		"new_password": "Admin@654321",
	})
	if change.Code != http.StatusOK {
		t.Fatalf("修改密码失败: %d %s", change.Code, change.Body.String())
	}
	if rec := f.request(t, http.MethodGet, "/api/dashboard/summary", token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("改密后旧令牌应返回 401，实际 %d", rec.Code)
	}
	if _, ok := f.login(t, "admin1", "Admin@123456"); ok {
		t.Fatal("旧密码不应继续登录")
	}
	if _, ok := f.login(t, "admin1", "Admin@654321"); !ok {
		t.Fatal("新密码应可登录")
	}
}

func TestJWTAuthRevokedWhenUserDisabledOrDeleted(t *testing.T) {
	f := newRouteFixture(t)
	createUser(t, f.db, "admin1", "Admin@123456", model.RoleAdmin)
	createUser(t, f.db, "viewer1", "Viewer@123456", model.RoleViewer)
	viewerID := f.userID(t, "viewer1")

	adminToken, ok := f.login(t, "admin1", "Admin@123456")
	if !ok {
		t.Fatal("管理员登录失败")
	}
	viewerToken, ok := f.login(t, "viewer1", "Viewer@123456")
	if !ok {
		t.Fatal("只读账号登录失败")
	}

	disable := f.request(t, http.MethodPut, fmt.Sprintf("/api/admin-users/%d", viewerID), adminToken,
		map[string]any{"enabled": false})
	if disable.Code != http.StatusOK {
		t.Fatalf("禁用账号失败: %d %s", disable.Code, disable.Body.String())
	}
	if rec := f.request(t, http.MethodGet, "/api/consumers", viewerToken, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("禁用后旧令牌应返回 401，实际 %d", rec.Code)
	}
	if _, ok := f.login(t, "viewer1", "Viewer@123456"); ok {
		t.Fatal("禁用账号不应登录成功")
	}

	enable := f.request(t, http.MethodPut, fmt.Sprintf("/api/admin-users/%d", viewerID), adminToken,
		map[string]any{"enabled": true})
	if enable.Code != http.StatusOK {
		t.Fatalf("启用账号失败: %d %s", enable.Code, enable.Body.String())
	}
	if rec := f.request(t, http.MethodGet, "/api/consumers", viewerToken, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("重新启用后旧令牌仍应返回 401，实际 %d", rec.Code)
	}

	viewerToken, ok = f.login(t, "viewer1", "Viewer@123456")
	if !ok {
		t.Fatal("重新启用后登录失败")
	}
	if del := f.request(t, http.MethodDelete, fmt.Sprintf("/api/admin-users/%d", viewerID), adminToken, nil); del.Code != http.StatusOK {
		t.Fatalf("删除账号失败: %d %s", del.Code, del.Body.String())
	}
	if rec := f.request(t, http.MethodGet, "/api/consumers", viewerToken, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("删除账号后旧令牌应返回 401，实际 %d", rec.Code)
	}
}

func TestJWTAuthRevokedOnRoleChangeButNotNickname(t *testing.T) {
	f := newRouteFixture(t)
	createUser(t, f.db, "admin1", "Admin@123456", model.RoleAdmin)
	createUser(t, f.db, "viewer1", "Viewer@123456", model.RoleViewer)
	viewerID := f.userID(t, "viewer1")

	adminToken, ok := f.login(t, "admin1", "Admin@123456")
	if !ok {
		t.Fatal("管理员登录失败")
	}
	viewerToken, ok := f.login(t, "viewer1", "Viewer@123456")
	if !ok {
		t.Fatal("只读账号登录失败")
	}

	nickname := f.request(t, http.MethodPut, fmt.Sprintf("/api/admin-users/%d", viewerID), adminToken,
		map[string]any{"nickname": "新昵称"})
	if nickname.Code != http.StatusOK {
		t.Fatalf("修改昵称失败: %d %s", nickname.Code, nickname.Body.String())
	}
	if rec := f.request(t, http.MethodGet, "/api/consumers", viewerToken, nil); rec.Code != http.StatusOK {
		t.Fatalf("仅改昵称不应吊销令牌，实际 %d", rec.Code)
	}

	role := f.request(t, http.MethodPut, fmt.Sprintf("/api/admin-users/%d", viewerID), adminToken,
		map[string]any{"role": model.RoleAdmin})
	if role.Code != http.StatusOK {
		t.Fatalf("修改角色失败: %d %s", role.Code, role.Body.String())
	}
	if rec := f.request(t, http.MethodGet, "/api/consumers", viewerToken, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("角色变化后旧令牌应返回 401，实际 %d", rec.Code)
	}

	viewerToken, ok = f.login(t, "viewer1", "Viewer@123456")
	if !ok {
		t.Fatal("角色变化后重新登录失败")
	}
	if rec := f.request(t, http.MethodGet, "/api/admin-users", viewerToken, nil); rec.Code != http.StatusOK {
		t.Fatalf("重新登录后应按新角色授权，实际 %d", rec.Code)
	}
}
