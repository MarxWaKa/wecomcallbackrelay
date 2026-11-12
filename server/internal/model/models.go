package model

import (
	"time"

	"gorm.io/gorm"
)

// 转发模式。
const (
	ForwardModeJSON       = "json"
	ForwardModeWecomXML   = "wecom_xml"
	ForwardModeWecomProxy = "wecom_proxy" // 原样透传企微加密请求，消费端共享入站密钥
)

// JSON 模式出站认证方式。
const (
	AuthTypeNone   = "none"
	AuthTypeBearer = "bearer"
	AuthTypeBasic  = "basic"
	AuthTypeHMAC   = "hmac"
)

// 投递任务状态。
const (
	TaskStatusPending = "pending"
	TaskStatusSending = "sending" // Worker 已抢占，正在投递
	TaskStatusSuccess = "success"
	TaskStatusFailed  = "failed" // 非重试型失败（如消费端被删除/禁用）
	TaskStatusDead    = "dead"
)

// 死信状态。
const (
	DeadStatusPending   = "pending"
	DeadStatusReplaying = "replaying"
	DeadStatusResolved  = "resolved"
	DeadStatusDiscarded = "discarded"
)

// 管理员角色。
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// AdminUser 管理后台账号。
type AdminUser struct {
	ID           uint64     `gorm:"primaryKey" json:"id"`
	Username     string     `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string     `gorm:"size:255;not null" json:"-"`
	Nickname     string     `gorm:"size:64" json:"nickname"`
	Role         string     `gorm:"size:32;not null;default:admin" json:"role"`
	Enabled      bool       `gorm:"not null" json:"enabled"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Consumer 下游消费端配置。
type Consumer struct {
	ID                  uint64         `gorm:"primaryKey" json:"id"`
	Name                string         `gorm:"size:100;not null" json:"name"`
	CallbackURL         string         `gorm:"size:500;not null" json:"callback_url"`
	ForwardMode         string         `gorm:"size:20;not null;default:json" json:"forward_mode"`
	AuthType            string         `gorm:"size:20;not null;default:none" json:"auth_type"`
	AuthConfig          string         `gorm:"type:text" json:"-"` // 加密后的 JSON
	WecomToken          string         `gorm:"size:512" json:"-"`  // 加密存储
	WecomEncodingAESKey string         `gorm:"size:1024" json:"-"` // 加密存储
	WecomCorpID         string         `gorm:"size:128" json:"wecom_corp_id"`
	WecomAgentID        string         `gorm:"size:64" json:"wecom_agent_id"` // 可选，用于测试事件的外层 AgentID
	Enabled             bool           `gorm:"not null" json:"enabled"`
	TimeoutMS           int            `gorm:"not null;default:5000" json:"timeout_ms"`
	MaxRetry            int            `gorm:"not null;default:5" json:"max_retry"`
	Remark              string         `gorm:"size:255" json:"remark"`
	CreatedBy           uint64         `json:"created_by"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	DeletedAt           gorm.DeletedAt `gorm:"index" json:"-"`
}

func (Consumer) TableName() string { return "consumers" }

// WecomConfig 企微通讯录同步回调参数。
type WecomConfig struct {
	ID             uint64         `gorm:"primaryKey" json:"id"`
	CorpID         string         `gorm:"size:128" json:"corp_id"`
	Secret         string         `gorm:"size:1024;not null" json:"-"` // 加密存储
	Token          string         `gorm:"size:512;not null" json:"-"`  // 加密存储
	EncodingAESKey string         `gorm:"size:1024;not null" json:"-"` // 加密存储
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`
}

func (WecomConfig) TableName() string { return "wecom_config" }

// RelayTask 一条事件 × 一个消费端 = 一条投递记录。
type RelayTask struct {
	ID               uint64     `gorm:"primaryKey" json:"id"`
	EventID          string     `gorm:"size:100;not null;uniqueIndex:uk_event_consumer,priority:1" json:"event_id"`
	ConsumerID       uint64     `gorm:"not null;uniqueIndex:uk_event_consumer,priority:2" json:"consumer_id"`
	ForwardMode      string     `gorm:"size:20;not null" json:"forward_mode"`
	AgentID          string     `gorm:"size:64" json:"agent_id"`
	RawXML           string     `gorm:"type:mediumtext" json:"raw_xml"`
	JSONPayload      string     `gorm:"type:longtext" json:"json_payload"`
	RawEncryptedBody string     `gorm:"type:mediumtext" json:"raw_encrypted_body"` // wecom_proxy 模式原始加密报文
	InboundQuery     string     `gorm:"type:text" json:"inbound_query"`            // wecom_proxy 模式原始 Query
	Status           string     `gorm:"size:20;not null;default:pending;index:idx_status_retry,priority:1" json:"status"`
	RetryCount       int        `gorm:"not null;default:0" json:"retry_count"`
	MaxRetry         int        `gorm:"not null;default:5" json:"max_retry"`
	NextRetryAt      *time.Time `gorm:"index:idx_status_retry,priority:2" json:"next_retry_at"`
	LastError        string     `gorm:"type:text" json:"last_error"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func (RelayTask) TableName() string { return "relay_tasks" }

// DeadLetter 重试耗尽、需人工介入的投递记录。
type DeadLetter struct {
	ID                  uint64     `gorm:"primaryKey" json:"id"`
	EventID             string     `gorm:"size:100;not null;index" json:"event_id"`
	ConsumerID          uint64     `gorm:"not null;index" json:"consumer_id"`
	ConsumerName        string     `gorm:"size:100" json:"consumer_name"`
	ForwardMode         string     `gorm:"size:20;not null" json:"forward_mode"`
	AgentID             string     `gorm:"size:64" json:"agent_id"`
	CallbackURL         string     `gorm:"size:500" json:"callback_url"`
	RawXML              string     `gorm:"type:mediumtext" json:"raw_xml"`
	RawEncryptedBody    string     `gorm:"type:mediumtext" json:"raw_encrypted_body"`
	InboundQuery        string     `gorm:"type:text" json:"inbound_query"`
	JSONPayload         string     `gorm:"type:longtext" json:"json_payload"`
	AuthType            string     `gorm:"size:20" json:"auth_type"`
	AuthConfig          string     `gorm:"type:text" json:"-"` // 加密快照
	WecomTokenSnapshot  string     `gorm:"size:512" json:"-"`  // 加密快照
	WecomAESKeySnapshot string     `gorm:"size:1024" json:"-"` // 加密快照
	WecomCorpID         string     `gorm:"size:128" json:"wecom_corp_id"`
	TimeoutMS           int        `gorm:"not null;default:5000" json:"timeout_ms"`
	LastError           string     `gorm:"type:text" json:"last_error"`
	RetryCount          int        `gorm:"not null;default:0" json:"retry_count"`
	Status              string     `gorm:"size:20;not null;default:pending;index" json:"status"`
	CreatedAt           time.Time  `json:"created_at"`
	ResolvedAt          *time.Time `json:"resolved_at"`
}

func (DeadLetter) TableName() string { return "dead_letters" }

// AuditLog 管理端关键操作审计。
type AuditLog struct {
	ID         uint64    `gorm:"primaryKey" json:"id"`
	AdminID    uint64    `gorm:"index" json:"admin_id"`
	AdminName  string    `gorm:"size:64" json:"admin_name"`
	Action     string    `gorm:"size:64" json:"action"`
	TargetType string    `gorm:"size:64" json:"target_type"`
	TargetID   uint64    `json:"target_id"`
	Detail     string    `gorm:"type:text" json:"detail"`
	Result     string    `gorm:"size:32" json:"result"`
	CreatedAt  time.Time `json:"created_at"`
}

func (AuditLog) TableName() string { return "audit_logs" }
