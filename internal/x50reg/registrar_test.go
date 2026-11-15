package x50reg

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeUpstream stands up both the API host and the shared-mailbox host and
// records the exact request the registrar makes.
type fakeUpstream struct {
	api  *httptest.Server
	mail *httptest.Server

	registerBody map[string]any
	registerAuth string
	verifyToken  string
	keyAuth      string
	keyBody      map[string]any

	registerCalls int32
	verifyCalls   int32
	loginCalls    int32
	keyCalls      int32
	meCalls       int32
	billingCalls  int32

	// mailAfter delays the confirm mail by N mailbox polls.
	mailAfter int32
	mailPolls int32

	linkToken       string
	open            bool
	rejectAllVerify bool
}

func newFake(t *testing.T) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{open: true, linkToken: "link-tok:1:abc"}
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/signup":
			json.NewEncoder(w).Encode(map[string]any{"open": f.open, "social": []string{"google", "github"}})
		case "/api/auth/register":
			atomic.AddInt32(&f.registerCalls, 1)
			f.registerAuth = r.Header.Get("Authorization")
			json.NewDecoder(r.Body).Decode(&f.registerBody)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"status": "verification_sent"})
		case "/api/auth/verify":
			atomic.AddInt32(&f.verifyCalls, 1)
			var body struct {
				Token string `json:"token"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			f.verifyToken = body.Token
			if f.rejectAllVerify || body.Token != f.linkToken {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid token"})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"token": "session-1"})
		case "/api/auth/login":
			atomic.AddInt32(&f.loginCalls, 1)
			json.NewEncoder(w).Encode(map[string]string{"token": "session-1"})
		case "/api/auth/me":
			atomic.AddInt32(&f.meCalls, 1)
			if r.Header.Get("Authorization") != "Token session-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"username": "u"})
		case "/api/keys":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			atomic.AddInt32(&f.keyCalls, 1)
			f.keyAuth = r.Header.Get("Authorization")
			json.NewDecoder(r.Body).Decode(&f.keyBody)
			json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "prefix": "sk-50x-abcd", "api_key": "sk-50x-abcdSECRET", "plan": "starter", "tier": "paid",
			})
		case "/api/billing":
			atomic.AddInt32(&f.billingCalls, 1)
			json.NewEncoder(w).Encode(map[string]any{"balance_usd": "100.000000000", "tier": "paid"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	f.mail = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("alias_email") == "" || r.URL.Query().Get("token") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "分享链接无效或已失效"})
			return
		}
		n := atomic.AddInt32(&f.mailPolls, 1)
		emails := []map[string]any{{
			"subject":     "Confirm your 50x AI account",
			"fromAddress": "noreply_at_50x_energy@icloud.com",
			"receivedAt":  time.Now().UTC().Format(time.RFC3339),
			"textContent": "Confirm: https://ai.50x.energy/?verify=" + f.linkToken + "\n",
		}}
		if n <= f.mailAfter {
			emails = nil
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"emails": emails}})
	}))
	t.Cleanup(func() { f.api.Close(); f.mail.Close() })
	return f
}

func newFakeRegistrar(t *testing.T, f *fakeUpstream) *Registrar {
	t.Helper()
	r, err := New(Config{
		APIBase:      f.api.URL,
		Origin:       "https://ai.50x.energy",
		MailBase:     f.mail.URL,
		Email:        "shellac-visitor.3@icloud.com",
		Password:     "pw-1",
		Mailbox:      Mailbox{Email: "shellac-visitor.3@icloud.com", Token: "share-tok"},
		KeyName:      "registrar",
		MailWait:     5 * time.Second,
		MailInterval: 10 * time.Millisecond,
		Timeout:      5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegisterRunsObservedFlowInOrder(t *testing.T) {
	f := newFake(t)
	r := newFakeRegistrar(t, f)
	acc, err := r.Register(context.Background())
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if acc.APIKey != "sk-50x-abcdSECRET" || acc.KeyID != 42 || acc.BalanceUSD != "100.000000000" {
		t.Fatalf("unexpected account: %+v", acc)
	}
	if f.registerBody["email"] != "shellac-visitor.3@icloud.com" || f.registerBody["password"] != "pw-1" {
		t.Fatalf("register body: %v", f.registerBody)
	}
	if len(f.registerBody) != 2 {
		t.Fatalf("register must send exactly email+password, got %v", f.registerBody)
	}
	if f.registerAuth != "" {
		t.Fatalf("register must be unauthenticated, got %q", f.registerAuth)
	}
	if f.verifyToken != f.linkToken {
		t.Fatalf("verify token %q", f.verifyToken)
	}
	if f.keyAuth != "Token session-1" {
		t.Fatalf("key auth %q", f.keyAuth)
	}
	if f.keyBody["name"] != "registrar" {
		t.Fatalf("key body %v", f.keyBody)
	}
	if f.keyCalls != 1 || f.verifyCalls != 1 || f.registerCalls != 1 {
		t.Fatalf("call counts reg=%d verify=%d key=%d", f.registerCalls, f.verifyCalls, f.keyCalls)
	}
}

func TestRegisterPollsUntilMailArrives(t *testing.T) {
	f := newFake(t)
	f.mailAfter = 3
	r := newFakeRegistrar(t, f)
	if _, err := r.Register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if f.mailPolls < 4 {
		t.Fatalf("expected >=4 polls, got %d", f.mailPolls)
	}
}

func TestRegisterRejectsClosedSignupWithoutMutation(t *testing.T) {
	f := newFake(t)
	f.open = false
	r := newFakeRegistrar(t, f)
	_, err := r.Register(context.Background())
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("want closed error, got %v", err)
	}
	if f.registerCalls != 0 || f.keyCalls != 0 {
		t.Fatalf("closed signup must not mutate: reg=%d key=%d", f.registerCalls, f.keyCalls)
	}
}

func TestRegisterWrongLinkTokenFailsBeforeKey(t *testing.T) {
	f := newFake(t)
	f.rejectAllVerify = true
	r := newFakeRegistrar(t, f)
	_, err := r.Register(context.Background())
	if err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("want verify error, got %v", err)
	}
	if f.keyCalls != 0 {
		t.Fatalf("no key may be created after a verify failure, got %d", f.keyCalls)
	}
}

func TestMailboxLineParsing(t *testing.T) {
	mb, err := ParseMailboxLine("shellac-visitor.3@icloud.com----https://icloud.mida.vip/messages/shellac-visitor.3%40icloud.com?token=abc123")
	if err != nil {
		t.Fatal(err)
	}
	if mb.Email != "shellac-visitor.3@icloud.com" || mb.Token != "abc123" {
		t.Fatalf("parsed %+v", mb)
	}
	if _, err := ParseMailboxLine(""); err == nil {
		t.Fatal("empty line must fail")
	}
	if _, err := ParseMailboxLine("a@b.com----https://x/y"); err == nil {
		t.Fatal("missing token must fail")
	}
	if _, err := ParseMailboxLine("https://icloud.mida.vip/messages/a%40b.com?token=t"); err != nil {
		t.Fatalf("bare share url: %v", err)
	}
}

func TestExtractVerifyToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://ai.50x.energy/?verify=eyJ1IjoyODY2fQ:1xSSB9:abc", "eyJ1IjoyODY2fQ:1xSSB9:abc"},
		{`href="https://ai.50x.energy/?verify=tok123">Confirm</a>`, "tok123"},
		{"no link here", ""},
	}
	for _, c := range cases {
		got, ok := extractVerifyToken(c.in)
		if c.want == "" {
			if ok {
				t.Fatalf("%q: unexpected token %q", c.in, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Fatalf("%q: got %q ok=%v", c.in, got, ok)
		}
	}
}

func TestWithStickySIDKeepsGatewayAndTimeout(t *testing.T) {
	in := "http://user-region-Rand-sid-14rHQ12m-t-10:pass@us-eu.zooproxy.com:5000"
	out := WithStickySID(in)
	if !strings.Contains(out, "us-eu.zooproxy.com:5000") || !strings.Contains(out, "-t-10") || !strings.Contains(out, "-region-Rand-") {
		t.Fatalf("gateway identity lost: %s", out)
	}
	if strings.Contains(out, "sid-14rHQ12m") {
		t.Fatalf("sid not rotated: %s", out)
	}
	if WithStickySID("") != "" {
		t.Fatal("empty stays empty")
	}
	if got := WithStickySID("http://u:p@h:1"); got != "http://u:p@h:1" {
		t.Fatalf("non-sticky proxy must pass through: %s", got)
	}
}

func TestProxyPoolRotatesAndQuarantines(t *testing.T) {
	p := NewProxyPool([]string{"a", "b"})
	if got := p.Pick(); got != "a" {
		t.Fatalf("first pick %q", got)
	}
	if got := p.Pick(); got != "b" {
		t.Fatalf("second pick %q", got)
	}
	p.MarkBad("a")
	if got := p.Pick(); got != "b" {
		t.Fatalf("bad proxy still served: %q", got)
	}
	p.MarkBad("b")
	if got := p.Pick(); got == "" {
		t.Fatal("pool must recover rather than stall")
	}
	if NewProxyPool(nil).Pick() != "" {
		t.Fatal("empty pool means direct egress")
	}
}

func TestClassifyProxyRejection(t *testing.T) {
	if ClassifyProxyResponse("HTTP/1.1 403 Forbidden") == nil {
		t.Fatal("403 must classify as a proxy rejection")
	}
	if ClassifyProxyResponse("HTTP/1.1 200 Connection established") != nil {
		t.Fatal("200 must not classify as a rejection")
	}
}

func TestRandomPasswordShape(t *testing.T) {
	for i := 0; i < 50; i++ {
		pw := RandomPassword()
		if len(pw) < 12 {
			t.Fatalf("too short: %q", pw)
		}
		if !strings.ContainsAny(pw, "!@") {
			t.Fatalf("no symbol: %q", pw)
		}
	}
}

func TestFormatErrorBodyIsBounded(t *testing.T) {
	err := errorFrom(400, []byte(strings.Repeat("x", 1000)))
	if len(err.Error()) > 400 {
		t.Fatalf("unbounded error: %d", len(err.Error()))
	}
}
