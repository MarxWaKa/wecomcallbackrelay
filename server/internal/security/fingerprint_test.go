package security

import "testing"

func TestKeyFingerprint(t *testing.T) {
	if KeyFingerprint("", "") != "" {
		t.Fatal("空配置指纹应为空")
	}
	a := KeyFingerprint("token-a", "aeskey-a")
	b := KeyFingerprint("token-a", "aeskey-a")
	c := KeyFingerprint("token-a", "aeskey-b")
	if len(a) != 12 {
		t.Fatalf("指纹长度应为 12，实际 %q", a)
	}
	if a != b {
		t.Fatal("相同配置指纹应一致")
	}
	if a == c {
		t.Fatal("不同配置指纹应不同")
	}
}
