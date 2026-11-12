package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const cipherPrefix = "enc:v1:"

// Cipher 使用 AES-256-GCM 加密敏感配置；密钥由 CONFIG_ENCRYPT_KEY 经 SHA-256 派生。
type Cipher struct {
	aead cipher.AEAD
}

func NewCipher(key string) (*Cipher, error) {
	if len(key) < 16 {
		return nil, errors.New("CONFIG_ENCRYPT_KEY 至少需要 16 个字符")
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, fmt.Errorf("初始化加密器失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES-GCM 失败: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt 加密明文，密文带 enc:v1: 前缀。
func (c *Cipher) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return cipherPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密配置；无前缀的值按历史明文原样返回，便于平滑迁移。
func (c *Cipher) Decrypt(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !strings.HasPrefix(stored, cipherPrefix) {
		return stored, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, cipherPrefix))
	if err != nil {
		return "", errors.New("敏感配置格式错误")
	}
	nonceSize := c.aead.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("敏感配置长度错误")
	}
	plain, err := c.aead.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return "", errors.New("敏感配置解密失败（请检查 CONFIG_ENCRYPT_KEY 是否变更）")
	}
	return string(plain), nil
}

// MaskSecret 对敏感值脱敏，仅保留末 4 位。
func MaskSecret(v string) string {
	if v == "" {
		return ""
	}
	runes := []rune(v)
	if len(runes) <= 4 {
		return "****"
	}
	return "****" + string(runes[len(runes)-4:])
}
