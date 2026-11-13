package deploysignupstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptAtRest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.json")
	secret := []byte("0123456789abcdef0123456789abcdef")

	st, err := Open(p, secret)
	if err != nil { t.Fatal(err) }
	if _, err := st.Add(Candidate{Email: "a@b.com", Password: "Sup3rSecret", SessionCookie: "eyJhbGciOi", SecretKey: "sk_live_xyz"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	txt := string(raw)
	for _, leak := range []string{"a@b.com", "Sup3rSecret", "eyJhbGciOi", "sk_live_xyz"} {
		if strings.Contains(txt, leak) {
			t.Fatalf("明文泄漏: %q 出现在落盘内容里\n%s", leak, txt)
		}
	}
	if !strings.Contains(txt, "data") { t.Fatalf("没有加密块") }

	// 重新打开应能解出
	st2, err := Open(p, secret)
	if err != nil { t.Fatal(err) }
	list := st2.List()
	if len(list) != 1 || list[0].Password != "Sup3rSecret" {
		t.Fatalf("解密失败: %+v", list)
	}
	// 错误密钥必须失败
	if _, err := Open(p, []byte("ffffffffffffffffffffffffffffffff")); err == nil {
		t.Fatal("错误密钥竟然打开成功")
	}
}
