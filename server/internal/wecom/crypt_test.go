package wecom

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestAESKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	return base64.StdEncoding.EncodeToString(key)[:43]
}

func newTestCrypto(t *testing.T) *Crypto {
	t.Helper()
	c, err := NewCrypto("test-token", newTestAESKey(t), "ww-test-corp")
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}
	return c
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := newTestCrypto(t)
	plain := `<xml><Event><![CDATA[change_contact]]></Event><UserID><![CDATA[zhangsan]]></UserID></xml>`

	encrypted, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if encrypted == plain {
		t.Fatal("密文不应等于明文")
	}
	decrypted, err := c.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if decrypted != plain {
		t.Fatalf("明文不一致: got %q want %q", decrypted, plain)
	}
}

func TestVerifyURL(t *testing.T) {
	c := newTestCrypto(t)
	echoStr, err := c.Encrypt("random-echo-string")
	if err != nil {
		t.Fatalf("加密 echostr 失败: %v", err)
	}
	ts := "1700000000"
	nonce := "nonce12345"
	sig := c.Signature(ts, nonce, echoStr)

	got, err := c.VerifyURL(sig, ts, nonce, echoStr)
	if err != nil {
		t.Fatalf("VerifyURL 失败: %v", err)
	}
	if got != "random-echo-string" {
		t.Fatalf("echostr 不一致: %q", got)
	}

	if _, err := c.VerifyURL("deadbeef", ts, nonce, echoStr); err != ErrInvalidSignature {
		t.Fatalf("错误签名应返回 ErrInvalidSignature，实际: %v", err)
	}
}

func TestEncryptMsgAndParse(t *testing.T) {
	c := newTestCrypto(t)
	plain := `<xml><Event><![CDATA[change_contact]]></Event><ChangeType>create_user</ChangeType></xml>`

	msg, err := c.EncryptMsg(plain, "", "")
	if err != nil {
		t.Fatalf("EncryptMsg 失败: %v", err)
	}
	if !strings.Contains(msg.XML, "<Encrypt>") || !strings.Contains(msg.XML, "MsgSignature") {
		t.Fatalf("加密 XML 结构异常: %s", msg.XML)
	}
	encrypt, err := ParseEncryptedEnvelope([]byte(msg.XML))
	if err != nil {
		t.Fatalf("解析加密 XML 失败: %v", err)
	}
	if encrypt != msg.Encrypt {
		t.Fatal("解析出的 Encrypt 与生成值不一致")
	}
	if !c.VerifySignature(msg.Signature, msg.Timestamp, msg.Nonce, encrypt) {
		t.Fatal("重新加密后的签名应可通过校验")
	}
	decrypted, err := c.Decrypt(encrypt)
	if err != nil {
		t.Fatalf("解密重新加密的消息失败: %v", err)
	}
	if decrypted != plain {
		t.Fatalf("重新加密后明文不一致: %q", decrypted)
	}

	// 消费端使用共享密钥时应能独立解密
	consumer, err := NewCrypto("test-token", newTestAESKeyFromCrypto(t, c), "ww-test-corp")
	if err != nil {
		t.Fatalf("创建消费端 Crypto 失败: %v", err)
	}
	if _, err := consumer.Decrypt(encrypt); err != nil {
		t.Fatalf("消费端解密失败: %v", err)
	}
}

// newTestAESKeyFromCrypto 将 Crypto 内部密钥还原为 43 位 EncodingAESKey。
func newTestAESKeyFromCrypto(t *testing.T, c *Crypto) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(c.aesKey)[:43]
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	c := newTestCrypto(t)
	encrypted, _ := c.Encrypt("hello")

	other, err := NewCrypto("test-token", newTestAESKey(t), "ww-test-corp")
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}
	if _, err := other.Decrypt(encrypted); err == nil {
		t.Fatal("使用错误密钥解密应失败")
	}
}

func TestDecryptWithWrongCorpIDFails(t *testing.T) {
	c := newTestCrypto(t)
	encrypted, _ := c.Encrypt("hello")

	other, err := NewCrypto("test-token", newTestAESKeyFromCrypto(t, c), "ww-other-corp")
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}
	if _, err := other.Decrypt(encrypted); err == nil {
		t.Fatal("receiveID 不匹配时应解密失败")
	}
}

func TestInvalidEncodingAESKey(t *testing.T) {
	if _, err := NewCrypto("token", "too-short", "corp"); err != ErrInvalidEncodingAESKey {
		t.Fatalf("非法 AESKey 应返回 ErrInvalidEncodingAESKey，实际: %v", err)
	}
}

// TestDecryptCrossImplementationVector 使用独立 Node.js 实现按企微官方算法
// （AES-256-CBC、PKCS#7 32 字节块、16B random + 4B length + msg + receiveId）
// 生成的固定向量，交叉验证签名与解密；不包含任何真实或官方示例 ID。
func TestDecryptCrossImplementationVector(t *testing.T) {
	const (
		corpID     = "ww-test-corp"
		token      = "test-token"
		aesKey     = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
		signature  = "53a4092ca51bdb41fb4128f48ee02039e439416a"
		timestamp  = "1409659813"
		nonce      = "1372623149"
		encryptMsg = "4j/AuRx71kQlxVlzbpsMWHFMAsfhXznmO5TH0Yo/UB0TVHEHSw9bRvBFLrnCwvFC4m5F2Wy+0xbeiBnoP1OZUtZ/vfY4DIQhYAie3jrW+abLTnS1aSvncyGdeQYcR6yRQ6QYJfNo/ynXngzKA7C74jh2tBNIuLUOBK2EfvdYN9MCEOWJCjCQedYbMp3h3GqBle4eCEqxY4BhAVmfyPCgGrtMyDOQhRPu1r/Gs4CTjxJoifNfFJ1j5ZZgrACSdjHyJcxRdIb2MYdCTXQoOVt0Hv9NSRB7779JceAmOSpxo8dJyrJce2sOf22PARbXnDpiAqKWOzcJhEkW22DofXb0E6fk4ipY/6zrBRv1oElU2pxq5E7dhYPlqDteQILmlohpkEDuySCL91UrC6vcs/E8y7I7etsD0jCE182aLK2rips="
	)

	crypt, err := NewCrypto(token, aesKey, corpID)
	if err != nil {
		t.Fatalf("创建交叉验证向量 Crypto 失败: %v", err)
	}
	if !crypt.VerifySignature(signature, timestamp, nonce, encryptMsg) {
		t.Fatal("交叉验证向量签名校验失败")
	}

	plain, err := crypt.Decrypt(encryptMsg)
	if err != nil {
		t.Fatalf("交叉验证向量解密失败: %v", err)
	}

	var msg struct {
		ToUserName   string `xml:"ToUserName"`
		FromUserName string `xml:"FromUserName"`
		CreateTime   int64  `xml:"CreateTime"`
		MsgType      string `xml:"MsgType"`
		Content      string `xml:"Content"`
		MsgID        string `xml:"MsgId"`
		AgentID      string `xml:"AgentID"`
	}
	if err := xml.Unmarshal([]byte(plain), &msg); err != nil {
		t.Fatalf("交叉验证向量明文 XML 解析失败: %v\n%s", err, plain)
	}
	if msg.ToUserName != corpID || msg.FromUserName != "mycreate" ||
		msg.CreateTime != 1409659813 || msg.MsgType != "text" ||
		msg.Content != "hello" || msg.MsgID != "4561255354251345929" || msg.AgentID != "218" {
		t.Fatalf("交叉验证向量解密结果不匹配: %+v", msg)
	}
}

func TestRandomNonceIsNumeric(t *testing.T) {
	nonce, err := RandomNonce(10)
	if err != nil {
		t.Fatalf("生成 nonce 失败: %v", err)
	}
	if len(nonce) != 10 {
		t.Fatalf("nonce 长度错误: %q", nonce)
	}
	for _, ch := range nonce {
		if ch < '0' || ch > '9' {
			t.Fatalf("nonce 应为纯数字，实际: %q", nonce)
		}
	}
}

func TestIsTimestampValid(t *testing.T) {
	now := time.Unix(1700000000, 0)
	cases := []struct {
		ts    string
		valid bool
	}{
		{"1700000000", true},
		{"1700000300", true},
		{"1699999700", true},
		{"1700000301", false},
		{"1699999699", false},
		{"not-a-number", false},
		{"", false},
	}
	for _, tc := range cases {
		got := IsTimestampValid(tc.ts, now, 300*time.Second)
		if got != tc.valid {
			t.Errorf("IsTimestampValid(%q)=%v, want %v", tc.ts, got, tc.valid)
		}
	}

	// 兼容 13 位毫秒时间戳（部分网关/服务商使用）。
	if !IsTimestampValid(strconv.FormatInt(now.UnixMilli(), 10), now, 300*time.Second) {
		t.Fatal("当前毫秒时间戳应通过校验")
	}
	if IsTimestampValid(strconv.FormatInt(now.Add(-10*time.Minute).UnixMilli(), 10), now, 300*time.Second) {
		t.Fatal("超窗毫秒时间戳应被拒绝")
	}
}
