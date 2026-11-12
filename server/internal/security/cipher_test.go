package security

import (
	"strings"
	"testing"
)

func TestCipherRoundTrip(t *testing.T) {
	c, err := NewCipher("test-encrypt-key-please-change")
	if err != nil {
		t.Fatalf("创建加密器失败: %v", err)
	}
	encrypted, err := c.Encrypt("super-secret-value")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if !strings.HasPrefix(encrypted, cipherPrefix) {
		t.Fatalf("密文缺少前缀: %s", encrypted)
	}
	decrypted, err := c.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if decrypted != "super-secret-value" {
		t.Fatalf("解密结果错误: %q", decrypted)
	}
}

func TestCipherEmptyAndLegacyPlaintext(t *testing.T) {
	c, _ := NewCipher("test-encrypt-key-please-change")
	v, err := c.Encrypt("")
	if err != nil || v != "" {
		t.Fatalf("空串加密应返回空串: %q %v", v, err)
	}
	v, err = c.Decrypt("legacy-plaintext")
	if err != nil || v != "legacy-plaintext" {
		t.Fatalf("历史明文应原样返回: %q %v", v, err)
	}
}

func TestCipherWrongKeyFails(t *testing.T) {
	c1, _ := NewCipher("test-encrypt-key-please-change")
	c2, _ := NewCipher("another-encrypt-key-value")
	encrypted, _ := c1.Encrypt("secret")
	if _, err := c2.Decrypt(encrypted); err == nil {
		t.Fatal("使用不同密钥解密应失败")
	}
}

func TestMaskSecret(t *testing.T) {
	if MaskSecret("") != "" {
		t.Fatal("空值脱敏应为空")
	}
	if MaskSecret("abc") != "****" {
		t.Fatalf("短值脱敏错误: %s", MaskSecret("abc"))
	}
	if MaskSecret("abcdefgh") != "****efgh" {
		t.Fatalf("长值脱敏错误: %s", MaskSecret("abcdefgh"))
	}
}
