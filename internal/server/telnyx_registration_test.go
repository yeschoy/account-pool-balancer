package server

import (
	"strings"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/telnyxsignupstore"
)

// The shipped export carries the Microsoft OAuth client id in the third field
// on every single line. Treating it as a share token silently breaks every
// mailbox, so the parser must route it to MailModeMsa.
func TestParseTelnyxLinesShapes(t *testing.T) {
	const clientID = "9e5f94bc-e8a4-4e73-b8be-63364c29d753"
	text := strings.Join([]string{
		"FelixMason1548@outlook.com----a4Pn6RWigJB----" + clientID + "----M.C533_BL2.0.U.MsaArtifacts.-CmM5jxr$$",
		"LilahMelendez1446@outlook.com----n2AjtHn87C----mDs2l5UJfmyQ0ixBKJX_O4U0KbSBG0R4NAil_UZs1-U",
		"",
		"# comment",
		"dup@x.com----pw----abc",
		"dup@x.com----pw----abc",
		"broken@x.com----pw",
		"noclient@x.com----pw----" + clientID,
	}, "\n")

	got := parseTelnyxLines(text)
	if len(got) != 3 {
		t.Fatalf("want 3 parsed lines, got %d: %+v", len(got), got)
	}

	if got[0].MailMode != telnyxsignupstore.MailModeMsa {
		t.Errorf("msa line mode = %q", got[0].MailMode)
	}
	if got[0].ClientID != clientID {
		t.Errorf("msa clientId = %q", got[0].ClientID)
	}
	if got[0].RefreshToken == "" {
		t.Error("msa refreshToken missing")
	}
	if got[0].ShareToken != "" {
		t.Error("msa line must not carry a share token")
	}

	if got[1].MailMode != telnyxsignupstore.MailModeShareToken {
		t.Errorf("share line mode = %q", got[1].MailMode)
	}
	if got[1].ShareToken != "mDs2l5UJfmyQ0ixBKJX_O4U0KbSBG0R4NAil_UZs1-U" {
		t.Errorf("share token = %q", got[1].ShareToken)
	}

	// dedup@ was seen once; broken@ had too few fields; noclient@ had a client
	// id with no refresh token.
	if got[2].Email != "dup@x.com" {
		t.Errorf("third entry = %q", got[2].Email)
	}
}

func TestJSONUnescapeRefreshToken(t *testing.T) {
	// The provider exports '!' unescaped in JSON, so the console must accept
	// the raw text as a JSON string value without corrupting it.
	var in struct {
		Text string `json:"text"`
	}
	line := `FelixMason1548@outlook.com----a4Pn6RWigJB----9e5f94bc-e8a4-4e73-b8be-63364c29d753----M.C533!AbC*$$`
	in.Text = line
	got := parseTelnyxLines(in.Text)
	if len(got) != 1 {
		t.Fatalf("parsed %d", len(got))
	}
	if !strings.HasSuffix(got[0].RefreshToken, "$$") {
		t.Errorf("refresh token tail lost: %q", got[0].RefreshToken)
	}
	if !strings.Contains(got[0].RefreshToken, "!") {
		t.Errorf("refresh token '!' lost: %q", got[0].RefreshToken)
	}
}

func TestAssignTelnyxProxiesPinsExisting(t *testing.T) {
	pool := []string{"http://p1:1", "http://p2:2"}
	existing := []telnyxsignupstore.Row{{ID: "a", ProxyURL: "http://p1:1"}}
	targets := []telnyxsignupstore.Row{
		{ID: "b"},                              // no binding: gets the least loaded
		{ID: "c", ProxyURL: "http://pinned:9"}, // keeps its binding
	}
	got := assignTelnyxProxies(existing, targets, pool)
	if _, ok := got["c"]; ok {
		t.Error("a pinned row must not be reassigned")
	}
	// p1 already has one row, so b lands on p2.
	if got["b"] != "http://p2:2" {
		t.Errorf("b -> %q, want p2", got["b"])
	}
}

func TestTelnyxViewRowHidesCredentials(t *testing.T) {
	row := telnyxsignupstore.Row{
		ID: "x", Email: "a@b.com",
		ShareToken:   "mDs2l5UJfmyQ0ixBKJX_O4U0KbSBG0R4NAil_UZs1-U",
		ClientID:     "9e5f94bc-e8a4-4e73-b8be-63364c29d753",
		RefreshToken: "M.C533_SECRET",
		APIKey:       "KEY01ABCDEF_SuperSecretPart",
		SessionToken: "gAAAAABsecret",
		Status:       telnyxsignupstore.Keyed,
		CreditsUSD:   "25",
	}
	v := telnyxViewRow(row)
	if strings.Contains(v.ShareToken, "UZs1-U") {
		t.Errorf("share token leaked in view: %q", v.ShareToken)
	}
	if v.KeyPrefix != "KEY01ABCDEF" {
		t.Errorf("key prefix = %q", v.KeyPrefix)
	}
	if !v.HasKey || !v.HasSession || !v.HasMsa || !v.HasShare {
		t.Errorf("flags wrong: %+v", v)
	}
	// Refresh token must never be projected at all.
	if strings.Contains(strings.ToLower(v.Error), "secret") {
		t.Error("error field leaked")
	}
}

func TestTelnyxStatusText(t *testing.T) {
	cases := map[telnyxsignupstore.Status]string{
		telnyxsignupstore.Queued:  "待注册",
		telnyxsignupstore.Waiting: "等待验证邮件",
		telnyxsignupstore.Keyed:   "已完成",
		telnyxsignupstore.Failed:  "失败",
	}
	for in, want := range cases {
		if got := telnyxStatusText(in); got != want {
			t.Errorf("%s -> %q, want %q", in, got, want)
		}
	}
}

func TestTelnyxErrorTextBounded(t *testing.T) {
	err := &boundedErr{}
	s := telnyxErrorText(err)
	// 400 bytes plus the "…" ellipsis the truncator appends.
	if len(s) > 403 {
		t.Errorf("error text not bounded: %d bytes", len(s))
	}
	if strings.Contains(s, "\n") {
		t.Error("newlines must be flattened")
	}
}

type boundedErr struct{}

func (b *boundedErr) Error() string { return strings.Repeat("x\ny", 400) }

// telnyxMgr used to call telnyxBaseCtx while holding telnyxMu, which takes the
// same lock: the very first /api/telnyx/* request self-deadlocked and every
// later request queued behind it. This test fails (by timing out) if that
// ordering comes back.
func TestTelnyxManagerInitDoesNotDeadlock(t *testing.T) {
	s := &Server{dataDir: t.TempDir()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := s.telnyxMgr(); err != nil {
			t.Errorf("telnyxMgr: %v", err)
			return
		}
		// A second call must return the cached manager, not re-init.
		if _, err := s.telnyxMgr(); err != nil {
			t.Errorf("telnyxMgr second call: %v", err)
		}
		if _, err := s.telnyxStore(); err != nil {
			t.Errorf("telnyxStore: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("telnyxMgr deadlocked on telnyxMu")
	}
}

// Concurrent handlers must not deadlock either: the manager, the store and the
// base context are all reachable from several endpoints at once.
func TestTelnyxConcurrentAccess(t *testing.T) {
	s := &Server{dataDir: t.TempDir()}
	var wg sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := s.telnyxMgr(); err != nil {
					t.Errorf("mgr: %v", err)
					return
				}
				if _, err := s.telnyxStore(); err != nil {
					t.Errorf("store: %v", err)
					return
				}
				_ = s.telnyxBaseCtx()
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent telnyx access deadlocked")
	}
}
