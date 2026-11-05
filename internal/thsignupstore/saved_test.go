package thsignupstore

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"account-pool-balancer/internal/thregister"
)

func TestSavedRowsAndProxyPoolSurviveEncryptedReopen(t *testing.T) {
	s, path := openTemp(t)
	proxies := []string{"http://user:private-proxy-password@127.0.0.1:8080", "socks5://user:private-proxy-password@127.0.0.1:1080"}
	if err := s.SaveProxies(proxies); err != nil {
		t.Fatal(err)
	}
	proxies[0] = "http://mutated:1"
	rows, err := s.AddSaved("saved", []Row{{Email: "saved@x.com", Password: "private-password", ProxyURL: s.Proxies()[0]}})
	if err != nil || len(rows) != 1 || rows[0].Status != Saved {
		t.Fatal("save failed", err)
	}
	id := rows[0].ID
	if _, err := s.Update(id, func(r *Row) error {
		r.Subscription = thregister.Subscription{Status: "free", Tier: "free", Name: "Free"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pool := s.Proxies()
	pool[0] = "http://mutated:2"
	reopened, err := Open(path, secret)
	if err != nil {
		t.Fatal(err)
	}
	row, _ := reopened.Get(id)
	if row.Status != Saved || row.Password != "private-password" || row.ProxyURL != reopened.Proxies()[0] || row.Subscription.Status != "free" || strings.Contains(row.ProxyURL, "mutated") {
		t.Fatal("saved binding/observation lost on reopen")
	}
	raw, _ := os.ReadFile(path)
	view, _ := json.Marshal(reopened.List())
	for _, secret := range []string{"private-password", "private-proxy-password"} {
		if strings.Contains(string(raw), secret) || strings.Contains(string(view), secret) {
			t.Fatal("credential leaked")
		}
	}
	direct, err := reopened.SetProxy(id, "")
	if err != nil || direct.Egress("http://default:8080") != "" {
		t.Fatal("explicit direct binding must not fall back to a later default")
	}
	legacy := Row{}
	if legacy.Egress("http://default:8080") != "http://default:8080" {
		t.Fatal("legacy empty egress must retain fallback compatibility")
	}
	if _, err := reopened.SetProxy(id, "http://new:8080"); err != nil {
		t.Fatal("saved proxy must be editable", err)
	}
	queued, err := reopened.Retry(id)
	if err != nil || queued.Status != Queued || queued.ProxyURL != "http://new:8080" {
		t.Fatal("saved row must explicitly queue without losing credentials", err)
	}
}

func TestInvalidProxyPoolDoesNotReplaceSavedPool(t *testing.T) {
	s, _ := openTemp(t)
	original := "http://one:8080"
	if err := s.SaveProxies([]string{original}); err != nil {
		t.Fatal(err)
	}
	for _, proxies := range [][]string{{""}, {"socks4://bad:1"}, {"http://valid:1", "bad"}, make([]string, 1001)} {
		if err := s.SaveProxies(proxies); err == nil {
			t.Fatal("invalid proxy pool accepted")
		}
		if got := s.Proxies(); len(got) != 1 || got[0] != original {
			t.Fatal("invalid pool replaced valid pool")
		}
	}
	if err := s.SaveProxies(nil); err != nil || len(s.Proxies()) != 0 {
		t.Fatal("explicit clear failed", err)
	}
}
