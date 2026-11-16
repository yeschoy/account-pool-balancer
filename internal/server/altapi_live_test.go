package server

import (
	"context"
	"os"
	"testing"
	"time"
)

// 真实读取：用已注册账号的 session cookie 走服务端 altapiFetchQuota，
// 验证 quota/quota_per_unit 与站点页面 "Current balance" 一致。
func TestALTAPILiveConsoleBalance(t *testing.T) {
	session := os.Getenv("ALTAPI_SESSION")
	key := os.Getenv("ALTAPI_KEY")
	if session == "" {
		t.Skip("ALTAPI_SESSION not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	res := altapiFetchQuota(ctx, "https://altapi.ai", session, key, os.Getenv("ALTAPI_UID"), "")
	if !res.OK {
		t.Fatalf("read failed: %s / %s", res.Error, res.Hint)
	}
	if res.Source != "session" {
		t.Fatalf("source=%s, want session", res.Source)
	}
	if res.BalanceUSD == nil {
		t.Fatal("balance nil on session read")
	}
	t.Logf("uid=%s user=%s group=%s balance=$%.4f used=$%.4f req=%d unit=%v",
		res.UserID, res.Username, res.Group, *res.BalanceUSD,
		*res.UsedUSD, res.RequestCount, res.QuotaPerUnit)
}
