package wecom

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	wxbizmsgcrypt "github.com/sbzhu/weworkapi_golang/wxbizmsgcrypt"
)

var (
	// ErrInvalidSignature 签名校验失败。
	ErrInvalidSignature = errors.New("签名校验失败")
	// ErrInvalidEncodingAESKey EncodingAESKey 不合法。
	ErrInvalidEncodingAESKey = errors.New("EncodingAESKey 无效（需为 43 位）")
)

// Crypto 封装企业微信官方 Golang 加解密库（XML 版）：
// https://github.com/sbzhu/weworkapi_golang
//
// 验签、解密、加密均委托给官方库完成；本类型额外保留签名计算、
// 随机 nonce 与时间戳窗口工具，供出站报文和测试使用。
type Crypto struct {
	token     string
	aesKey    []byte
	receiveID string
	official  *wxbizmsgcrypt.WXBizMsgCrypt
}

// NewCrypto 创建加解密器；receiveID 为企微 CorpID（第三方场景为 suiteid）。
func NewCrypto(token, encodingAESKey, receiveID string) (*Crypto, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Token 不能为空")
	}
	if len(encodingAESKey) != 43 {
		return nil, ErrInvalidEncodingAESKey
	}
	key, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil || len(key) != 32 {
		return nil, ErrInvalidEncodingAESKey
	}
	official := wxbizmsgcrypt.NewWXBizMsgCrypt(token, encodingAESKey, receiveID, wxbizmsgcrypt.XmlType)
	return &Crypto{
		token:     token,
		aesKey:    key,
		receiveID: receiveID,
		official:  official,
	}, nil
}

// Signature 计算企微消息签名：SHA1(sort(token, timestamp, nonce, encrypt))。
// 官方库内部使用同一算法；此方法用于出站签名校验与诊断。
func (c *Crypto) Signature(timestamp, nonce, encrypt string) string {
	arr := []string{c.token, timestamp, nonce, encrypt}
	sort.Strings(arr)
	h := sha1.New()
	for _, item := range arr {
		_, _ = h.Write([]byte(item))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// VerifySignature 恒定时间比较签名。
func (c *Crypto) VerifySignature(signature, timestamp, nonce, encrypt string) bool {
	expected := c.Signature(timestamp, nonce, encrypt)
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(signature)), []byte(expected)) == 1
}

// VerifyURL 调用官方库校验 URL 验证请求并解密 echostr。
func (c *Crypto) VerifyURL(msgSignature, timestamp, nonce, echostr string) (string, error) {
	out, err := c.callOfficial(func() ([]byte, *wxbizmsgcrypt.CryptError) {
		return c.official.VerifyURL(msgSignature, timestamp, nonce, echostr)
	})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// DecryptMsg 调用官方库校验签名并解密完整回调 XML 报文。
func (c *Crypto) DecryptMsg(msgSignature, timestamp, nonce string, postData []byte) (string, error) {
	out, err := c.callOfficial(func() ([]byte, *wxbizmsgcrypt.CryptError) {
		return c.official.DecryptMsg(msgSignature, timestamp, nonce, postData)
	})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Decrypt 解密单个 Encrypt 字符串（主要用于测试与兼容旧调用）。
// 内部使用官方库 DecryptMsg：构造最小 XML 并用 token、固定 timestamp/nonce 生成签名。
func (c *Crypto) Decrypt(encrypted string) (string, error) {
	const timestamp = "0"
	const nonce = "0"
	envelope := "<xml><ToUserName><![CDATA[]]></ToUserName><Encrypt><![CDATA[" + encrypted + "]]></Encrypt></xml>"
	return c.DecryptMsg(c.Signature(timestamp, nonce, encrypted), timestamp, nonce, []byte(envelope))
}

// Encrypt 使用官方库加密明文，返回 Base64 密文。
func (c *Crypto) Encrypt(plain string) (string, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := RandomNonce(10)
	if err != nil {
		return "", err
	}
	msg, err := c.EncryptMsg(plain, timestamp, nonce)
	if err != nil {
		return "", err
	}
	return msg.Encrypt, nil
}

// EncryptedMessage 企微兼容模式出站所需的加密结果与签名参数。
type EncryptedMessage struct {
	XML       string
	Encrypt   string
	Signature string
	Timestamp string
	Nonce     string
}

// EncryptMsg 调用官方库重新加密明文 XML，并解析出签名、时间戳与 nonce。
// 官方库返回的是被动回复 XML，本方法只提取字段；出站回调报文由 sender 构造。
func (c *Crypto) EncryptMsg(plain, timestamp, nonce string) (*EncryptedMessage, error) {
	if timestamp == "" {
		timestamp = strconv.FormatInt(time.Now().Unix(), 10)
	}
	if nonce == "" {
		var err error
		nonce, err = RandomNonce(10)
		if err != nil {
			return nil, err
		}
	}

	out, err := c.callOfficial(func() ([]byte, *wxbizmsgcrypt.CryptError) {
		return c.official.EncryptMsg(plain, timestamp, nonce)
	})
	if err != nil {
		return nil, err
	}

	var msg struct {
		Encrypt   string `xml:"Encrypt"`
		Signature string `xml:"MsgSignature"`
		Timestamp string `xml:"TimeStamp"`
		Nonce     string `xml:"Nonce"`
	}
	if err := xml.Unmarshal(out, &msg); err != nil {
		return nil, fmt.Errorf("解析官方库加密结果失败: %w", err)
	}
	return &EncryptedMessage{
		XML:       string(out),
		Encrypt:   msg.Encrypt,
		Signature: msg.Signature,
		Timestamp: msg.Timestamp,
		Nonce:     msg.Nonce,
	}, nil
}

// callOfficial 统一处理官方库错误码与潜在的 panic（畸形密文可能导致库内部切片越界）。
func (c *Crypto) callOfficial(fn func() ([]byte, *wxbizmsgcrypt.CryptError)) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = nil
			err = fmt.Errorf("企微官方加解密库异常: %v", r)
		}
	}()
	out, cryptErr := fn()
	if cryptErr != nil {
		return nil, mapCryptError(cryptErr)
	}
	return out, nil
}

func mapCryptError(e *wxbizmsgcrypt.CryptError) error {
	if e == nil {
		return nil
	}
	if e.ErrCode == wxbizmsgcrypt.ValidateSignatureError {
		return ErrInvalidSignature
	}
	return fmt.Errorf("企微加解密失败(code=%d): %s", e.ErrCode, e.ErrMsg)
}

// Envelope 入站加密 XML 的外层结构。
type Envelope struct {
	XMLName    xml.Name `xml:"xml"`
	ToUserName string   `xml:"ToUserName"`
	AgentID    string   `xml:"AgentID"`
	Encrypt    string   `xml:"Encrypt"`
}

// ParseEnvelope 解析入站加密 XML 的完整外层结构。
func ParseEnvelope(body []byte) (*Envelope, error) {
	var env Envelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("解析加密 XML 失败: %w", err)
	}
	if strings.TrimSpace(env.Encrypt) == "" {
		return nil, errors.New("加密 XML 中缺少 Encrypt 字段")
	}
	return &env, nil
}

// ParseEncryptedEnvelope 从入站 XML 中提取 Encrypt 字段。
func ParseEncryptedEnvelope(body []byte) (string, error) {
	env, err := ParseEnvelope(body)
	if err != nil {
		return "", err
	}
	return env.Encrypt, nil
}

// RandomNonce 生成指定长度的数字随机串，与企微回调的 nonce 形式保持一致。
func RandomNonce(length int) (string, error) {
	const charset = "0123456789"
	if length <= 0 {
		length = 10
	}
	result := make([]byte, length)
	max := big.NewInt(int64(len(charset)))
	for i := range result {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("生成随机串失败: %w", err)
		}
		result[i] = charset[n.Int64()]
	}
	return string(result), nil
}

// IsTimestampValid 校验时间戳是否在允许偏差窗口内。
// 使用上下界比较而非差值计算，避免超大时间戳导致的整数溢出绕过。
func IsTimestampValid(timestamp string, now time.Time, skew time.Duration) bool {
	ts, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil || ts <= 0 {
		return false
	}
	// 企业微信原生回调为秒级时间戳；兼容部分网关/服务商传入的 13 位毫秒时间戳。
	// 签名校验始终使用请求中的原始字符串，此处的换算只用于时间窗口判断。
	if ts > 1_000_000_000_000 {
		ts /= 1000
	}
	skewSeconds := int64(skew / time.Second)
	if skewSeconds < 0 {
		return false
	}
	nowUnix := now.Unix()
	return ts >= nowUnix-skewSeconds && ts <= nowUnix+skewSeconds
}
