package security

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
)

// KeyFingerprint 返回 Token + EncodingAESKey 的短指纹，用于中间件与消费端核对
// 配置是否一致，同时避免在页面/日志中暴露密钥原文。
// 计算方式：SHA-256("wecom-relay|" + token + "\x00" + aesKey) 的前 12 位十六进制。
func KeyFingerprint(token, aesKey string) string {
	if token == "" && aesKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("wecom-relay|" + token + "\x00" + aesKey))
	return hex.EncodeToString(sum[:6])
}

// ContentHash 返回报文内容的短指纹，用于确认入站、快照、出站内容未被修改。
// 计算方式：SHA-256(data) 的前 16 位十六进制。
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// ContentMD5 返回完整 MD5，便于与消费端/网关的 MD5 日志直接对比。
func ContentMD5(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}
