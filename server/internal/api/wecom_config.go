package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/model"
	"wecomrelay/internal/security"
	"wecomrelay/internal/wecom"
)

// WecomConfigHandler 企微回调参数管理；本期只维护一条记录。
type WecomConfigHandler struct {
	db     *gorm.DB
	cipher *security.Cipher
	logger *slog.Logger
}

func NewWecomConfigHandler(db *gorm.DB, cipher *security.Cipher, logger *slog.Logger) *WecomConfigHandler {
	return &WecomConfigHandler{db: db, cipher: cipher, logger: logger}
}

type wecomConfigRequest struct {
	CorpID         string `json:"corp_id"`
	Secret         string `json:"secret"`
	Token          string `json:"token"`
	EncodingAESKey string `json:"encoding_aes_key"`
}

type wecomConfigView struct {
	ID                   uint64    `json:"id"`
	CorpID               string    `json:"corp_id"`
	SecretMasked         string    `json:"secret_masked"`
	TokenMasked          string    `json:"token_masked"`
	EncodingAESKeyMasked string    `json:"encoding_aes_key_masked"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// List 返回企微参数（脱敏）。
func (h *WecomConfigHandler) List(c *gin.Context) {
	var configs []model.WecomConfig
	if err := h.db.Order("id ASC").Find(&configs).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询企微参数失败")
		return
	}
	views := make([]wecomConfigView, 0, len(configs))
	for _, cfg := range configs {
		views = append(views, h.toView(cfg))
	}
	OK(c, gin.H{"list": views})
}

// Create 新增企微参数；只允许一条记录。
func (h *WecomConfigHandler) Create(c *gin.Context) {
	var count int64
	if err := h.db.Model(&model.WecomConfig{}).Count(&count).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "查询企微参数失败")
		return
	}
	if count > 0 {
		Fail(c, http.StatusConflict, "企微参数已存在，请直接修改")
		return
	}
	var req wecomConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	if err := validateWecomConfig(&req); err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	secret, err := h.cipher.Encrypt(strings.TrimSpace(req.Secret))
	if err != nil {
		Fail(c, http.StatusInternalServerError, "Secret 加密失败")
		return
	}
	token, err := h.cipher.Encrypt(strings.TrimSpace(req.Token))
	if err != nil {
		Fail(c, http.StatusInternalServerError, "Token 加密失败")
		return
	}
	aesKey, err := h.cipher.Encrypt(strings.TrimSpace(req.EncodingAESKey))
	if err != nil {
		Fail(c, http.StatusInternalServerError, "EncodingAESKey 加密失败")
		return
	}
	cfg := model.WecomConfig{
		CorpID:         strings.TrimSpace(req.CorpID),
		Secret:         secret,
		Token:          token,
		EncodingAESKey: aesKey,
	}
	if err := h.db.Create(&cfg).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "保存企微参数失败")
		return
	}
	writeAudit(h.db, c, "wecom_config.create", "wecom_config", cfg.ID, "创建企微参数", "success")
	OK(c, h.toView(cfg))
}

// Update 修改企微参数；敏感字段留空或传 ****** 表示保持不变。
func (h *WecomConfigHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var cfg model.WecomConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "企微参数不存在")
		return
	}
	var req wecomConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}

	existingSecret, _ := h.cipher.Decrypt(cfg.Secret)
	existingToken, _ := h.cipher.Decrypt(cfg.Token)
	existingAESKey, _ := h.cipher.Decrypt(cfg.EncodingAESKey)

	secret := strings.TrimSpace(req.Secret)
	if isMaskedOrEmpty(secret) {
		secret = existingSecret
	}
	token := strings.TrimSpace(req.Token)
	if isMaskedOrEmpty(token) {
		token = existingToken
	}
	aesKey := strings.TrimSpace(req.EncodingAESKey)
	if isMaskedOrEmpty(aesKey) {
		aesKey = existingAESKey
	}
	corpID := strings.TrimSpace(req.CorpID)
	if corpID == "" {
		corpID = cfg.CorpID
	}

	merged := wecomConfigRequest{CorpID: corpID, Secret: secret, Token: token, EncodingAESKey: aesKey}
	if err := validateWecomConfig(&merged); err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}

	encSecret, _ := h.cipher.Encrypt(secret)
	encToken, _ := h.cipher.Encrypt(token)
	encAESKey, _ := h.cipher.Encrypt(aesKey)
	if err := h.db.Model(&cfg).Updates(map[string]any{
		"corp_id":          corpID,
		"secret":           encSecret,
		"token":            encToken,
		"encoding_aes_key": encAESKey,
	}).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "修改企微参数失败")
		return
	}
	if err := h.db.First(&cfg, id).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "读取企微参数失败")
		return
	}
	writeAudit(h.db, c, "wecom_config.update", "wecom_config", cfg.ID, "修改企微参数", "success")
	OK(c, h.toView(cfg))
}

// Delete 删除企微参数。
func (h *WecomConfigHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var cfg model.WecomConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		Fail(c, http.StatusNotFound, "企微参数不存在")
		return
	}
	if err := h.db.Delete(&cfg).Error; err != nil {
		Fail(c, http.StatusInternalServerError, "删除企微参数失败")
		return
	}
	writeAudit(h.db, c, "wecom_config.delete", "wecom_config", cfg.ID, "删除企微参数", "success")
	OK(c, gin.H{"message": "已删除"})
}

func (h *WecomConfigHandler) toView(cfg model.WecomConfig) wecomConfigView {
	secret, _ := h.cipher.Decrypt(cfg.Secret)
	token, _ := h.cipher.Decrypt(cfg.Token)
	aesKey, _ := h.cipher.Decrypt(cfg.EncodingAESKey)
	return wecomConfigView{
		ID:                   cfg.ID,
		CorpID:               cfg.CorpID,
		SecretMasked:         security.MaskSecret(secret),
		TokenMasked:          security.MaskSecret(token),
		EncodingAESKeyMasked: security.MaskSecret(aesKey),
		CreatedAt:            cfg.CreatedAt,
		UpdatedAt:            cfg.UpdatedAt,
	}
}

func validateWecomConfig(req *wecomConfigRequest) error {
	req.CorpID = strings.TrimSpace(req.CorpID)
	req.Secret = strings.TrimSpace(req.Secret)
	req.Token = strings.TrimSpace(req.Token)
	req.EncodingAESKey = strings.TrimSpace(req.EncodingAESKey)
	if req.CorpID == "" {
		return fmt.Errorf("CorpID 不能为空")
	}
	if req.Secret == "" {
		return fmt.Errorf("Secret 不能为空")
	}
	if req.Token == "" {
		return fmt.Errorf("Token 不能为空")
	}
	if _, err := wecom.NewCrypto(req.Token, req.EncodingAESKey, req.CorpID); err != nil {
		return fmt.Errorf("EncodingAESKey 不合法: %w", err)
	}
	return nil
}
