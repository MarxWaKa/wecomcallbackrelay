package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/bootstrap"
	"wecomrelay/internal/config"
	"wecomrelay/internal/database"
	"wecomrelay/internal/model"
	"wecomrelay/internal/relay"
	"wecomrelay/internal/router"
	"wecomrelay/internal/security"
)

func main() {
	if err := run(); err != nil {
		slog.Error("服务启动失败", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	switch strings.ToLower(cfg.GinMode) {
	case "debug", "dev":
		gin.SetMode(gin.DebugMode)
	case "test":
		gin.SetMode(gin.TestMode)
	default:
		gin.SetMode(gin.ReleaseMode)
	}

	db, err := database.Connect(cfg.DB, gin.Mode() == gin.DebugMode)
	if err != nil {
		return err
	}
	if err := database.Migrate(db); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	if err := bootstrap.SeedAdmin(db, cfg, logger); err != nil {
		return err
	}

	cipher, err := security.NewCipher(cfg.EncryptKey)
	if err != nil {
		return err
	}
	logKeyFingerprints(db, cipher, logger)
	jwtManager := security.NewJWTManager(cfg.JWTSecret, cfg.JWTExpire)
	sender := relay.NewSender(logger)
	worker := relay.NewWorker(db, cipher, sender, logger, cfg.WorkerInterval, cfg.WorkerConcurrency, cfg.RetryBackoff)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	worker.Start(ctx)

	engine, err := router.New(router.Dependencies{
		Config: cfg,
		DB:     db,
		Cipher: cipher,
		JWT:    jwtManager,
		Sender: sender,
		Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("初始化路由失败: %w", err)
	}

	server := &http.Server{
		Addr:              ":" + cfg.ServerPort,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      180 * time.Second, // 为死信批量重推等长耗时管理操作留出余量
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("服务已启动",
			"port", cfg.ServerPort,
			"worker_interval", cfg.WorkerInterval.String(),
			"retry_backoff", fmt.Sprint(cfg.RetryBackoff),
		)
		if listenErr := server.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			errCh <- listenErr
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("收到退出信号，开始优雅关闭")
	case listenErr := <-errCh:
		return fmt.Errorf("HTTP 服务异常退出: %w", listenErr)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP 服务关闭超时", "error", err)
	}
	worker.Wait()
	sqlDB, err := db.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
	logger.Info("服务已退出")
	return nil
}

// logKeyFingerprints 启动时输出密钥指纹，便于与消费端核对；
// 指纹不可逆，不包含 Token/AESKey 原文。
func logKeyFingerprints(db *gorm.DB, cipher *security.Cipher, logger *slog.Logger) {
	var cfg model.WecomConfig
	if err := db.Order("id ASC").First(&cfg).Error; err == nil {
		token, _ := cipher.Decrypt(cfg.Token)
		aesKey, _ := cipher.Decrypt(cfg.EncodingAESKey)
		logger.Info("企微参数密钥指纹",
			"corp_id", cfg.CorpID,
			"key_fingerprint", security.KeyFingerprint(token, aesKey),
		)
	}

	var consumers []model.Consumer
	if err := db.Find(&consumers).Error; err != nil {
		return
	}
	for _, consumer := range consumers {
		token, _ := cipher.Decrypt(consumer.WecomToken)
		aesKey, _ := cipher.Decrypt(consumer.WecomEncodingAESKey)
		logger.Info("消费端密钥指纹",
			"consumer_id", consumer.ID,
			"consumer", consumer.Name,
			"forward_mode", consumer.ForwardMode,
			"corp_id", consumer.WecomCorpID,
			"key_fingerprint", security.KeyFingerprint(token, aesKey),
		)
	}
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv})
	return slog.New(handler)
}
