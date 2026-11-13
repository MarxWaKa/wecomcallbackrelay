package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DBConfig 数据库连接配置。
type DBConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
}

// DSN 返回 MySQL 连接串。
func (d DBConfig) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s",
		d.User, d.Password, d.Host, d.Port, d.Name)
}

// Config 服务全部配置，均来自环境变量。
type Config struct {
	ServerPort              string
	GinMode                 string
	LogLevel                string
	LogRawEvents            bool
	DB                      DBConfig
	JWTSecret               string
	JWTExpire               time.Duration
	EncryptKey              string
	AdminUsername           string
	AdminPassword           string
	WorkerInterval          time.Duration
	WorkerConcurrency       int
	RetryBackoff            []time.Duration
	PushRecordRetentionDays int
	CallbackMaxSkew         time.Duration
	CORSOrigins             []string
}

// Load 读取并校验环境变量配置。
func Load() (*Config, error) {
	c := &Config{
		ServerPort:   env("SERVER_PORT", "8888"),
		GinMode:      env("GIN_MODE", "release"),
		LogLevel:     env("LOG_LEVEL", "info"),
		LogRawEvents: envBool("RELAY_LOG_RAW_EVENTS", false),
		DB: DBConfig{
			Host:     env("DB_HOST", "127.0.0.1"),
			Port:     env("DB_PORT", "3306"),
			User:     env("DB_USER", "root"),
			Password: env("DB_PASSWORD", ""),
			Name:     env("DB_NAME", "wecom_relay"),
		},
		JWTSecret:               env("JWT_SECRET", ""),
		JWTExpire:               time.Duration(envInt("JWT_EXPIRE_HOURS", 24)) * time.Hour,
		EncryptKey:              env("CONFIG_ENCRYPT_KEY", ""),
		AdminUsername:           env("ADMIN_USERNAME", "admin"),
		AdminPassword:           env("ADMIN_PASSWORD", ""),
		WorkerInterval:          time.Duration(envInt("WORKER_INTERVAL_SECONDS", 2)) * time.Second,
		WorkerConcurrency:       envInt("WORKER_CONCURRENCY", 8),
		PushRecordRetentionDays: envInt("PUSH_RECORD_RETENTION_DAYS", 30),
		CallbackMaxSkew:         time.Duration(envInt("CALLBACK_MAX_SKEW_SECONDS", 300)) * time.Second,
		CORSOrigins:             splitCSV(env("CORS_ALLOW_ORIGINS", "*")),
	}

	retryBackoff, err := parseBackoff(env("RETRY_BACKOFF_SECONDS", "1,2,4,8,16"))
	if err != nil {
		return nil, fmt.Errorf("RETRY_BACKOFF_SECONDS 配置错误: %w", err)
	}
	c.RetryBackoff = retryBackoff

	if len(c.JWTSecret) < 16 {
		return nil, fmt.Errorf("JWT_SECRET 至少需要 16 个字符")
	}
	if len(c.EncryptKey) < 16 {
		return nil, fmt.Errorf("CONFIG_ENCRYPT_KEY 至少需要 16 个字符")
	}
	if c.DB.Host == "" || c.DB.Port == "" || c.DB.User == "" || c.DB.Name == "" {
		return nil, fmt.Errorf("数据库连接配置不完整")
	}
	if c.WorkerConcurrency < 1 {
		c.WorkerConcurrency = 1
	}
	if c.WorkerInterval < time.Second {
		c.WorkerInterval = time.Second
	}
	if c.PushRecordRetentionDays < 0 {
		return nil, fmt.Errorf("PUSH_RECORD_RETENTION_DAYS 不能为负数")
	}
	if c.CallbackMaxSkew <= 0 {
		c.CallbackMaxSkew = 300 * time.Second
	}
	return c, nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func envInt(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseBackoff 解析逗号分隔的秒数序列，如 "1,2,4,8,16" 或固定 "1800"。
func parseBackoff(raw string) ([]time.Duration, error) {
	parts := splitCSV(raw)
	if len(parts) == 0 {
		return nil, fmt.Errorf("不能为空")
	}
	out := make([]time.Duration, 0, len(parts))
	for _, part := range parts {
		seconds, err := strconv.Atoi(part)
		if err != nil || seconds <= 0 {
			return nil, fmt.Errorf("%q 不是正整数秒数", part)
		}
		out = append(out, time.Duration(seconds)*time.Second)
	}
	return out, nil
}
