package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"wecomrelay/internal/model"
	"wecomrelay/internal/security"
	"wecomrelay/internal/wecom"
)

// CallbackHandler 处理企微 GET 验证与 POST 事件回调。
type CallbackHandler struct {
	db           *gorm.DB
	cipher       *security.Cipher
	logger       *slog.Logger
	maxSkew      time.Duration
	logRawEvents bool
}

func NewCallbackHandler(db *gorm.DB, cipher *security.Cipher, logger *slog.Logger, maxSkew time.Duration, logRawEvents bool) *CallbackHandler {
	return &CallbackHandler{
		db:           db,
		cipher:       cipher,
		logger:       logger,
		maxSkew:      maxSkew,
		logRawEvents: logRawEvents,
	}
}

// Verify 处理企微回调地址验证（GET）。
func (h *CallbackHandler) Verify(c *gin.Context) {
	query := c.Request.URL.Query()
	msgSignature := query.Get("msg_signature")
	timestamp := query.Get("timestamp")
	nonce := query.Get("nonce")
	echoStr := query.Get("echostr")
	if msgSignature == "" || timestamp == "" || nonce == "" || echoStr == "" {
		callbackFail(c, http.StatusBadRequest, "缺少必填参数")
		return
	}
	if !wecom.IsTimestampValid(timestamp, time.Now(), h.maxSkew) {
		callbackFail(c, http.StatusForbidden, "timestamp 超出允许窗口")
		return
	}

	cfg, crypt, err := h.loadCrypto()
	if err != nil {
		h.logger.Error("URL 验证失败：企微参数不可用", "error", err)
		callbackFail(c, http.StatusForbidden, "服务未正确配置企微参数")
		return
	}

	plain, err := crypt.VerifyURL(msgSignature, timestamp, nonce, echoStr)
	if err != nil {
		h.logger.Warn("URL 验证失败：验签或解密不通过", "corp_id", cfg.CorpID, "error", err)
		callbackFail(c, http.StatusForbidden, "验签或解密失败")
		return
	}
	h.logger.Info("企微回调 URL 验证通过", "corp_id", cfg.CorpID)
	c.String(http.StatusOK, "%s", plain)
}

// Receive 处理企微通讯录事件回调（POST）。
func (h *CallbackHandler) Receive(c *gin.Context) {
	query := c.Request.URL.Query()
	msgSignature := query.Get("msg_signature")
	timestamp := query.Get("timestamp")
	nonce := query.Get("nonce")
	if msgSignature == "" || timestamp == "" || nonce == "" {
		callbackFail(c, http.StatusBadRequest, "缺少必填参数")
		return
	}
	if !wecom.IsTimestampValid(timestamp, time.Now(), h.maxSkew) {
		callbackFail(c, http.StatusForbidden, "timestamp 超出允许窗口")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	if err != nil {
		callbackFail(c, http.StatusBadRequest, "读取请求体失败")
		return
	}
	envelope, err := wecom.ParseEnvelope(body)
	if err != nil {
		h.logger.Warn("企微回调请求体解析失败", "error", err)
		callbackFail(c, http.StatusBadRequest, "加密报文格式错误")
		return
	}
	// 排查开关：LOG_LEVEL=debug 时输出企微入站原始报文；不包含 Token/AESKey/Secret。
	h.logger.Debug("企微回调入站请求",
		"remote_ip", c.ClientIP(),
		"content_type", c.GetHeader("Content-Type"),
		"query", c.Request.URL.RawQuery,
		"body_len", len(body),
		"body_md5", security.ContentMD5(body),
		"body_sha256", security.ContentHash(body),
		"query_md5", security.ContentMD5([]byte(c.Request.URL.RawQuery)),
		"query_sha256", security.ContentHash([]byte(c.Request.URL.RawQuery)),
		"to_user_name", envelope.ToUserName,
		"agent_id", envelope.AgentID,
		"encrypt_len", len(envelope.Encrypt),
		"body", string(body),
	)

	cfg, crypt, err := h.loadCrypto()
	if err != nil {
		h.logger.Error("事件回调失败：企微参数不可用", "error", err)
		callbackFail(c, http.StatusForbidden, "服务未正确配置企微参数")
		return
	}
	// 官方加解密库在 DecryptMsg 内部完成签名校验、AES 解密与 receiveID 校验。
	plain, err := crypt.DecryptMsg(msgSignature, timestamp, nonce, body)
	if err != nil {
		h.logger.Warn("事件回调验签/解密失败", "corp_id", cfg.CorpID, "error", err)
		callbackFail(c, http.StatusForbidden, "验签或解密失败")
		return
	}

	msg, err := wecom.ParseCallbackMessage(plain)
	if err != nil {
		// 按需求：明文解析失败也返回 200，避免企微重试风暴，但不入库、不转发。
		h.logger.Error("回调明文解析失败", "error", err)
		c.String(http.StatusOK, "success")
		return
	}

	corpID := cfg.CorpID
	if corpID == "" {
		corpID = msg.ToUserName
	}
	eventID := wecom.MakeEventID(msg, plain)
	plainAttrs := []any{
		"event_id", eventID,
		"plain_len", len(plain),
		"plain_sha256", security.ContentHash([]byte(plain)),
	}
	if h.logRawEvents {
		plainAttrs = append(plainAttrs, "plain", plain)
	}
	h.logger.Debug("企微回调解密明文", plainAttrs...)
	if msg.ToUserName != "" && cfg.CorpID != "" && msg.ToUserName != cfg.CorpID {
		h.logger.Warn("回调 ToUserName 与配置 CorpID 不一致",
			"event_id", eventID,
			"to_user_name", msg.ToUserName,
			"configured_corp_id", cfg.CorpID,
		)
	}
	event := wecom.BuildEvent(msg, corpID, eventID)
	payload, err := wecom.MarshalEvent(event)
	if err != nil {
		h.logger.Error("构造 JSON 载荷失败", "event_id", eventID, "error", err)
		callbackFail(c, http.StatusInternalServerError, "error")
		return
	}

	var consumers []model.Consumer
	if err := h.db.Where("enabled = ?", true).Find(&consumers).Error; err != nil {
		h.logger.Error("查询消费端失败", "event_id", eventID, "error", err)
		callbackFail(c, http.StatusInternalServerError, "error")
		return
	}

	if len(consumers) == 0 {
		h.logger.Warn("收到回调但没有启用的消费端", "event_id", eventID, "change_type", msg.ChangeType)
		c.String(http.StatusOK, "success")
		return
	}

	tasks := make([]model.RelayTask, 0, len(consumers))
	for _, consumer := range consumers {
		maxRetry := consumer.MaxRetry
		if maxRetry <= 0 {
			maxRetry = 5
		}
		task := model.RelayTask{
			EventID:      eventID,
			ConsumerID:   consumer.ID,
			ConsumerName: consumer.Name,
			CallbackURL:  consumer.CallbackURL,
			ForwardMode:  consumer.ForwardMode,
			AgentID:      envelope.AgentID,
			RawXML:       plain,
			JSONPayload:  string(payload),
			Status:       model.TaskStatusPending,
			MaxRetry:     maxRetry,
		}
		if consumer.ForwardMode == model.ForwardModeWecomProxy {
			task.RawEncryptedBody = string(body)
			task.InboundQuery = c.Request.URL.RawQuery
		}
		tasks = append(tasks, task)
	}
	if err := h.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&tasks).Error; err != nil {
		h.logger.Error("回调任务入库失败", "event_id", eventID, "error", err)
		callbackFail(c, http.StatusInternalServerError, "error")
		return
	}

	h.logger.Info("企微回调已入库",
		"event_id", eventID,
		"change_type", msg.ChangeType,
		"user_id", msg.UserID,
		"consumers", len(consumers),
	)
	c.String(http.StatusOK, "success")
}

func (h *CallbackHandler) loadCrypto() (*model.WecomConfig, *wecom.Crypto, error) {
	var cfg model.WecomConfig
	if err := h.db.Order("id ASC").First(&cfg).Error; err != nil {
		return nil, nil, fmt.Errorf("未找到企微参数: %w", err)
	}
	token, err := h.cipher.Decrypt(cfg.Token)
	if err != nil {
		return nil, nil, fmt.Errorf("Token 解密失败: %w", err)
	}
	aesKey, err := h.cipher.Decrypt(cfg.EncodingAESKey)
	if err != nil {
		return nil, nil, fmt.Errorf("EncodingAESKey 解密失败: %w", err)
	}
	h.logger.Debug("企微参数密钥指纹",
		"corp_id", cfg.CorpID,
		"key_fingerprint", security.KeyFingerprint(token, aesKey),
	)
	crypt, err := wecom.NewCrypto(token, aesKey, strings.TrimSpace(cfg.CorpID))
	if err != nil {
		return nil, nil, err
	}
	return &cfg, crypt, nil
}

func callbackFail(c *gin.Context, status int, msg string) {
	c.String(status, "%s", msg)
}
