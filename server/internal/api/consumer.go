package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/middleware"
	"wecomrelay/internal/model"
	"wecomrelay/internal/relay"
	"wecomrelay/internal/security"
	"wecomrelay/internal/wecom"
)

// ConsumerHandler 消费端管理。
type ConsumerHandler struct {
	db     *gorm.DB
	cipher *security.Cipher
	sender *relay.Sender
	logger *slog.Logger
}

func NewConsumerHandler(db *gorm.DB, cipher *security.Cipher, sender *relay.Sender, logger *slog.Logger) *ConsumerHandler {
	return &ConsumerHandler{db: db, cipher: cipher, sender: sender, logger: logger}
}

type consumerRequest struct {
	Name                string            `json:"name"`
	CallbackURL         string            `json:"callback_url"`
	ForwardMode         string            `json:"forward_mode"`
	AuthType            string            `json:"auth_type"`
	AuthConfig          map[string]string `json:"auth_config"`
	WecomToken          string            `json:"wecom_token"`
	WecomEncodingAESKey string            `json:"wecom_encoding_aes_key"`
	WecomCorpID         string            `json:"wecom_corp_id"`
	WecomAgentID        string            `json:"wecom_agent_id"`
	Enabled             *bool             `json:"enabled"`
	TimeoutMS           int               `json:"timeout_ms"`
	MaxRetry            int               `json:"max_retry"`
	Remark              string            `json:"remark"`
}

type consumerView struct {
	ID                        uint64            `json:"id"`
	Name                      string            `json:"name"`
	CallbackURL               string            `json:"callback_url"`
	ForwardMode               string            `json:"forward_mode"`
	AuthType                  string            `json:"auth_type"`
	AuthConfig                map[string]string `json:"auth_config"`
	WecomTokenMasked          string            `json:"wecom_token_masked"`
	WecomEncodingAESKeyMasked string            `json:"wecom_encoding_aes_key_masked"`
	WecomKeyFingerprint       string            `json:"wecom_key_fingerprint"`
	WecomCorpID               string            `json:"wecom_corp_id"`
	WecomAgentID              string            `json:"wecom_agent_id"`
	Enabled                   bool              `json:"enabled"`
	TimeoutMS                 int               `json:"timeout_ms"`
	MaxRetry                  int               `json:"max_retry"`
	Remark                    string            `json:"remark"`
	CreatedAt                 time.Time         `json:"created_at"`
	UpdatedAt                 time.Time         `json:"updated_at"`
}

// List 消费端列表。
func (h *ConsumerHandler) List(c *gin.Context) {
	page, size := PageParams(c)
	query := h.db.Model(&model.Consumer{})

	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("name LIKE ? OR callback_url LIKE ?", like, like)
	}
	if mode := strings.TrimSpace(c.Query("forward_mode")); mode != "" {
		query = query.Where("forward_mode = ?", mode)
	}
	if enabled, ok := boolQuery(c, "enabled"); ok {
		query = query.Where("enabled = ?", enabled)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询消费端失败")
		return
	}
	var consumers []model.Consumer
	if err := query.Order("id ASC").Offset((page - 1) * size).Limit(size).Find(&consumers).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询消费端失败")
		return
	}
	views := make([]consumerView, 0, len(consumers))
	for _, consumer := range consumers {
		views = append(views, h.toView(consumer))
	}
	OK(c, PageData{List: views, Total: total, Page: page, PageSize: size})
}

// Get 消费端详情。
func (h *ConsumerHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var consumer model.Consumer
	if err := h.db.First(&consumer, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "消费端不存在")
		return
	}
	OK(c, h.toView(consumer))
}

// Create 新增消费端。
func (h *ConsumerHandler) Create(c *gin.Context) {
	var req consumerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	if err := validateConsumerBasics(&req); err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}

	mode, authType := normalizeModes(req.ForwardMode, req.AuthType)
	if err := validateWecomMode(mode, req.WecomToken, req.WecomEncodingAESKey, req.WecomCorpID); err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	authConfigJSON, err := buildAuthConfig(authType, req.AuthConfig, nil)
	if err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}

	encAuthConfig, err := h.cipher.Encrypt(authConfigJSON)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "认证配置加密失败")
		return
	}
	encToken, err := h.cipher.Encrypt(strings.TrimSpace(req.WecomToken))
	if err != nil {
		Fail(c, http.StatusInternalServerError, "Token 加密失败")
		return
	}
	encAESKey, err := h.cipher.Encrypt(strings.TrimSpace(req.WecomEncodingAESKey))
	if err != nil {
		Fail(c, http.StatusInternalServerError, "EncodingAESKey 加密失败")
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	consumer := model.Consumer{
		Name:                strings.TrimSpace(req.Name),
		CallbackURL:         strings.TrimSpace(req.CallbackURL),
		ForwardMode:         mode,
		AuthType:            authType,
		AuthConfig:          encAuthConfig,
		WecomToken:          encToken,
		WecomEncodingAESKey: encAESKey,
		WecomCorpID:         strings.TrimSpace(req.WecomCorpID),
		WecomAgentID:        strings.TrimSpace(req.WecomAgentID),
		Enabled:             enabled,
		TimeoutMS:           normalizedTimeout(req.TimeoutMS),
		MaxRetry:            normalizedMaxRetry(req.MaxRetry),
		Remark:              strings.TrimSpace(req.Remark),
		CreatedBy:           middleware.UserID(c),
	}
	if err := h.db.Create(&consumer).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "创建消费端失败")
		return
	}
	writeAudit(h.db, c, "consumer.create", "consumer", consumer.ID, "创建消费端 "+consumer.Name, "success")
	OK(c, h.toView(consumer))
}

// Update 修改消费端；敏感字段留空或传 ****** 表示保持不变。
func (h *ConsumerHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var consumer model.Consumer
	if err := h.db.First(&consumer, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "消费端不存在")
		return
	}
	var req consumerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	if err := validateConsumerBasics(&req); err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}

	mode, authType := normalizeModes(req.ForwardMode, req.AuthType)

	// 解密历史敏感值，用于“留空不修改”。
	existingToken, _ := h.cipher.Decrypt(consumer.WecomToken)
	existingAESKey, _ := h.cipher.Decrypt(consumer.WecomEncodingAESKey)
	existingAuthJSON, _ := h.cipher.Decrypt(consumer.AuthConfig)

	token := strings.TrimSpace(req.WecomToken)
	if isMaskedOrEmpty(token) {
		token = existingToken
	}
	aesKey := strings.TrimSpace(req.WecomEncodingAESKey)
	if isMaskedOrEmpty(aesKey) {
		aesKey = existingAESKey
	}
	corpID := strings.TrimSpace(req.WecomCorpID)
	if corpID == "" {
		corpID = consumer.WecomCorpID
	}
	agentID := strings.TrimSpace(req.WecomAgentID)
	if agentID == "" {
		agentID = consumer.WecomAgentID
	}
	if err := validateWecomMode(mode, token, aesKey, corpID); err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}

	var existingAuth map[string]string
	if authType == consumer.AuthType {
		_ = json.Unmarshal([]byte(existingAuthJSON), &existingAuth)
	}
	authConfigJSON, err := buildAuthConfig(authType, req.AuthConfig, existingAuth)
	if err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	encAuthConfig, err := h.cipher.Encrypt(authConfigJSON)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "认证配置加密失败")
		return
	}
	encToken, err := h.cipher.Encrypt(token)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "Token 加密失败")
		return
	}
	encAESKey, err := h.cipher.Encrypt(aesKey)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "EncodingAESKey 加密失败")
		return
	}

	updates := map[string]any{
		"name":                   strings.TrimSpace(req.Name),
		"callback_url":           strings.TrimSpace(req.CallbackURL),
		"forward_mode":           mode,
		"auth_type":              authType,
		"auth_config":            encAuthConfig,
		"wecom_token":            encToken,
		"wecom_encoding_aes_key": encAESKey,
		"wecom_corp_id":          corpID,
		"wecom_agent_id":         agentID,
		"timeout_ms":             normalizedTimeout(req.TimeoutMS),
		"max_retry":              normalizedMaxRetry(req.MaxRetry),
		"remark":                 strings.TrimSpace(req.Remark),
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.db.Model(&consumer).Updates(updates).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "修改消费端失败")
		return
	}
	if err := h.db.First(&consumer, id).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "读取消费端失败")
		return
	}
	writeAudit(h.db, c, "consumer.update", "consumer", consumer.ID, "修改消费端 "+consumer.Name, "success")
	OK(c, h.toView(consumer))
}

// Delete 删除消费端（软删除）。
func (h *ConsumerHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var consumer model.Consumer
	if err := h.db.First(&consumer, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "消费端不存在")
		return
	}
	if err := h.db.Delete(&consumer).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "删除消费端失败")
		return
	}
	writeAudit(h.db, c, "consumer.delete", "consumer", consumer.ID, "删除消费端 "+consumer.Name, "success")
	OK(c, gin.H{"message": "已删除"})
}

// Test 向消费端发送一条测试事件，用于验证连通性与转发模式配置。
func (h *ConsumerHandler) Test(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var consumer model.Consumer
	if err := h.db.First(&consumer, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "消费端不存在")
		return
	}
	now := time.Now().Unix()
	if consumer.ForwardMode == model.ForwardModeWecomProxy {
		h.testProxyConsumer(c, consumer, now)
		return
	}

	authConfig, _ := h.cipher.Decrypt(consumer.AuthConfig)
	wecomToken, _ := h.cipher.Decrypt(consumer.WecomToken)
	wecomAESKey, _ := h.cipher.Decrypt(consumer.WecomEncodingAESKey)

	eventID := fmt.Sprintf("evt_test_%d", now)
	jsonPayload := fmt.Sprintf(`{"event_id":%q,"source":"wecom_contact_sync","event_type":"test","change_type":"test","corp_id":%q,"create_time":%d,"payload":{}}`,
		eventID, consumer.WecomCorpID, now)
	rawXML := fmt.Sprintf(`<xml><ToUserName><![CDATA[%s]]></ToUserName><CreateTime>%d</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[test]]></Event></xml>`,
		consumer.WecomCorpID, now)

	target := relay.Target{
		ConsumerID:          consumer.ID,
		Name:                consumer.Name,
		CallbackURL:         consumer.CallbackURL,
		ForwardMode:         consumer.ForwardMode,
		AuthType:            consumer.AuthType,
		AuthConfig:          authConfig,
		WecomToken:          wecomToken,
		WecomEncodingAESKey: wecomAESKey,
		WecomCorpID:         consumer.WecomCorpID,
		TimeoutMS:           consumer.TimeoutMS,
	}
	statusCode, elapsed, err := h.sender.Deliver(c.Request.Context(), target, relay.Payload{
		EventID:     eventID,
		AgentID:     consumer.WecomAgentID,
		RawXML:      rawXML,
		JSONPayload: jsonPayload,
	})
	fingerprint := security.KeyFingerprint(wecomToken, wecomAESKey)
	if err != nil {
		h.logger.Warn("消费端连通性测试失败",
			"consumer_id", consumer.ID,
			"mode", consumer.ForwardMode,
			"corp_id", consumer.WecomCorpID,
			"key_fingerprint", fingerprint,
			"http_status", statusCode,
			"error", err,
		)
		OK(c, gin.H{
			"success":         false,
			"status_code":     statusCode,
			"latency_ms":      elapsed.Milliseconds(),
			"error":           err.Error(),
			"corp_id":         consumer.WecomCorpID,
			"key_fingerprint": fingerprint,
		})
		return
	}
	OK(c, gin.H{
		"success":         true,
		"status_code":     statusCode,
		"latency_ms":      elapsed.Milliseconds(),
		"corp_id":         consumer.WecomCorpID,
		"key_fingerprint": fingerprint,
	})
}

// testProxyConsumer 使用「企微参数」的密钥生成一条原始格式请求，验证消费端能否解密。
func (h *ConsumerHandler) testProxyConsumer(c *gin.Context, consumer model.Consumer, now int64) {
	var globalCfg model.WecomConfig
	if err := h.db.Order("id ASC").First(&globalCfg).Error; err != nil {
		Fail(c, http.StatusBadRequest, "原样透传测试需要先在「企微参数」中配置回调参数")
		return
	}
	token, err := h.cipher.Decrypt(globalCfg.Token)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "企微 Token 解密失败")
		return
	}
	aesKey, err := h.cipher.Decrypt(globalCfg.EncodingAESKey)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "企微 EncodingAESKey 解密失败")
		return
	}
	crypt, err := wecom.NewCrypto(token, aesKey, globalCfg.CorpID)
	if err != nil {
		Fail(c, http.StatusBadRequest, "企微参数密钥不合法: "+err.Error())
		return
	}

	rawXML := fmt.Sprintf(`<xml><ToUserName><![CDATA[%s]]></ToUserName><CreateTime>%d</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[test]]></Event></xml>`,
		globalCfg.CorpID, now)
	encrypted, err := crypt.EncryptMsg(rawXML, "", "")
	if err != nil {
		Fail(c, http.StatusInternalServerError, "生成测试密文失败: "+err.Error())
		return
	}
	body := relay.BuildWecomCallbackBody(globalCfg.CorpID, consumer.WecomAgentID, encrypted.Encrypt)
	query := "msg_signature=" + url.QueryEscape(encrypted.Signature) +
		"&timestamp=" + url.QueryEscape(encrypted.Timestamp) +
		"&nonce=" + url.QueryEscape(encrypted.Nonce)

	target := relay.Target{
		ConsumerID:  consumer.ID,
		Name:        consumer.Name,
		CallbackURL: consumer.CallbackURL,
		ForwardMode: model.ForwardModeWecomProxy,
		TimeoutMS:   consumer.TimeoutMS,
	}
	statusCode, elapsed, deliverErr := h.sender.DeliverProxy(c.Request.Context(), target, body, query)
	fingerprint := security.KeyFingerprint(token, aesKey)
	if deliverErr != nil {
		h.logger.Warn("原样透传连通性测试失败",
			"consumer_id", consumer.ID,
			"corp_id", globalCfg.CorpID,
			"key_fingerprint", fingerprint,
			"http_status", statusCode,
			"error", deliverErr,
		)
		OK(c, gin.H{
			"success":         false,
			"status_code":     statusCode,
			"latency_ms":      elapsed.Milliseconds(),
			"error":           deliverErr.Error(),
			"corp_id":         globalCfg.CorpID,
			"key_fingerprint": fingerprint,
			"agent_id":        consumer.WecomAgentID,
		})
		return
	}
	OK(c, gin.H{
		"success":         true,
		"status_code":     statusCode,
		"latency_ms":      elapsed.Milliseconds(),
		"corp_id":         globalCfg.CorpID,
		"key_fingerprint": fingerprint,
		"agent_id":        consumer.WecomAgentID,
	})
}

func (h *ConsumerHandler) toView(consumer model.Consumer) consumerView {
	wecomToken, _ := h.cipher.Decrypt(consumer.WecomToken)
	wecomAESKey, _ := h.cipher.Decrypt(consumer.WecomEncodingAESKey)
	authJSON, _ := h.cipher.Decrypt(consumer.AuthConfig)

	authConfig := map[string]string{}
	if authJSON != "" {
		_ = json.Unmarshal([]byte(authJSON), &authConfig)
	}
	return consumerView{
		ID:                        consumer.ID,
		Name:                      consumer.Name,
		CallbackURL:               consumer.CallbackURL,
		ForwardMode:               consumer.ForwardMode,
		AuthType:                  consumer.AuthType,
		AuthConfig:                maskAuthConfig(consumer.AuthType, authConfig),
		WecomTokenMasked:          security.MaskSecret(wecomToken),
		WecomEncodingAESKeyMasked: security.MaskSecret(wecomAESKey),
		WecomKeyFingerprint:       security.KeyFingerprint(wecomToken, wecomAESKey),
		WecomCorpID:               consumer.WecomCorpID,
		WecomAgentID:              consumer.WecomAgentID,
		Enabled:                   consumer.Enabled,
		TimeoutMS:                 consumer.TimeoutMS,
		MaxRetry:                  consumer.MaxRetry,
		Remark:                    consumer.Remark,
		CreatedAt:                 consumer.CreatedAt,
		UpdatedAt:                 consumer.UpdatedAt,
	}
}

func validateConsumerBasics(req *consumerRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	req.CallbackURL = strings.TrimSpace(req.CallbackURL)
	if req.Name == "" || len(req.Name) > 100 {
		return fmt.Errorf("消费端名称不能为空且不超过 100 个字符")
	}
	if len(req.CallbackURL) > 500 || !validCallbackURL(req.CallbackURL) {
		return fmt.Errorf("回调地址必须是合法的 http/https URL")
	}
	switch strings.TrimSpace(req.ForwardMode) {
	case "", model.ForwardModeJSON, model.ForwardModeWecomXML, model.ForwardModeWecomProxy:
	default:
		return fmt.Errorf("转发模式仅支持 json、wecom_xml 或 wecom_proxy")
	}
	mode := strings.TrimSpace(req.ForwardMode)
	if mode != model.ForwardModeWecomXML && mode != model.ForwardModeWecomProxy {
		switch strings.TrimSpace(req.AuthType) {
		case "", model.AuthTypeNone, model.AuthTypeBearer, model.AuthTypeBasic, model.AuthTypeHMAC:
		default:
			return fmt.Errorf("认证方式仅支持 none/bearer/basic/hmac")
		}
	}
	if req.TimeoutMS < 0 || req.TimeoutMS > 60000 {
		return fmt.Errorf("超时时间需在 0-60000 毫秒之间")
	}
	if req.MaxRetry < 0 || req.MaxRetry > 20 {
		return fmt.Errorf("最大重试次数需在 0-20 之间")
	}
	return nil
}

func normalizeModes(mode, authType string) (string, string) {
	switch strings.TrimSpace(mode) {
	case model.ForwardModeWecomXML:
		mode = model.ForwardModeWecomXML
	case model.ForwardModeWecomProxy:
		mode = model.ForwardModeWecomProxy
	case "", model.ForwardModeJSON:
		mode = model.ForwardModeJSON
	default:
		mode = model.ForwardModeJSON
	}
	if mode == model.ForwardModeWecomXML || mode == model.ForwardModeWecomProxy {
		return mode, model.AuthTypeNone
	}
	switch strings.TrimSpace(authType) {
	case model.AuthTypeBearer, model.AuthTypeBasic, model.AuthTypeHMAC:
		return mode, strings.TrimSpace(authType)
	default:
		return mode, model.AuthTypeNone
	}
}

func validateWecomMode(mode, token, aesKey, corpID string) error {
	if mode != model.ForwardModeWecomXML {
		return nil
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("企微兼容模式必须配置 Token")
	}
	if strings.TrimSpace(corpID) == "" {
		return fmt.Errorf("企微兼容模式必须配置 CorpID")
	}
	if _, err := wecom.NewCrypto(token, aesKey, corpID); err != nil {
		return fmt.Errorf("企微兼容模式密钥不合法: %w", err)
	}
	return nil
}

func buildAuthConfig(authType string, input, existing map[string]string) (string, error) {
	if authType == model.AuthTypeNone {
		return "{}", nil
	}
	merged := map[string]string{}
	for key, value := range existing {
		merged[key] = value
	}
	for key, value := range input {
		if isMaskedOrEmpty(value) {
			continue
		}
		merged[key] = value
	}
	switch authType {
	case model.AuthTypeBearer:
		if merged["token"] == "" {
			return "", fmt.Errorf("bearer 认证必须填写 Token")
		}
		return marshalAuthConfig(map[string]string{"token": merged["token"]})
	case model.AuthTypeBasic:
		if merged["username"] == "" || merged["password"] == "" {
			return "", fmt.Errorf("basic 认证必须填写用户名和密码")
		}
		return marshalAuthConfig(map[string]string{
			"username": merged["username"],
			"password": merged["password"],
		})
	case model.AuthTypeHMAC:
		if merged["secret"] == "" {
			return "", fmt.Errorf("hmac 认证必须填写 Secret")
		}
		return marshalAuthConfig(map[string]string{"secret": merged["secret"]})
	default:
		return "{}", nil
	}
}

func marshalAuthConfig(v map[string]string) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func maskAuthConfig(authType string, cfg map[string]string) map[string]string {
	out := map[string]string{}
	switch authType {
	case model.AuthTypeBearer:
		if cfg["token"] != "" {
			out["token"] = "******"
		}
	case model.AuthTypeBasic:
		out["username"] = cfg["username"]
		if cfg["password"] != "" {
			out["password"] = "******"
		}
	case model.AuthTypeHMAC:
		if cfg["secret"] != "" {
			out["secret"] = "******"
		}
	}
	return out
}

func normalizedTimeout(v int) int {
	if v <= 0 {
		return 5000
	}
	return v
}

func normalizedMaxRetry(v int) int {
	if v <= 0 {
		return 5
	}
	return v
}
