package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

type healthReplyTransport struct {
	base   http.RoundTripper
	status int
	body   string
	header http.Header
	retry  bool
	calls  atomic.Int32
}

func (tr *healthReplyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/auth/v1/user" {
		n := tr.calls.Add(1)
		status, body := tr.status, tr.body
		if tr.retry && n == 1 {
			status, body = 401, `{"error":"invalid JWT"}`
		}
		return &http.Response{StatusCode: status, Header: tr.header.Clone(), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}
	return tr.base.RoundTrip(req)
}

func TestPerchHealthStructuredBanned(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body          string
		header        http.Header
		retry, banned bool
	}{
		{name: "errorCode", status: 403, body: `{"errorCode":"banned"}`, banned: true},
		{name: "error_code", status: 401, body: `{"error_code":"banned"}`, banned: true},
		{name: "code after refresh", status: 403, body: `{"code":"banned"}`, retry: true, banned: true},
		{name: "plain error", status: 403, body: `{"error":"banned"}`},
		{name: "message", status: 403, body: `{"message":"account banned"}`},
		{name: "header only", status: 403, body: `{}`, header: http.Header{"X-Sb-Error-Code": []string{"banned"}}},
		{name: "other code", status: 403, body: `{"code":"not_banned"}`},
		{name: "temporary", status: 503, body: `{"code":"banned"}`},
		{name: "ok", status: 200, body: `{"id":"health-user"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newPerchEnv(t)
			e.fake.seed("health-user")
			e.importFiles(map[string][]byte{"account.json": []byte(exportDoc("health-user", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
			a := e.findByUser("health-user")
			tr := &healthReplyTransport{base: &rewriteTransport{target: e.fake.srv.Listener.Addr().String()}, status: tc.status, body: tc.body, header: tc.header, retry: tc.retry}
			e.srv.perch.keeper.SetClient(perchClientFor(&http.Client{Transport: tr}))
			e.srv.checkPerchBatch(context.Background(), []config.Account{a}, nil)
			v := e.srv.pool.Views(e.srv.cfg.Get())[a.ID]
			got := v.State != nil && v.State.HardQuarantined && v.State.RuleID == "perch-banned"
			if got != tc.banned {
				t.Fatalf("banned=%v, state=%+v", got, v.State)
			}
			if tc.retry && tr.calls.Load() != 2 {
				t.Fatalf("User calls = %d, want 2", tr.calls.Load())
			}
			if tc.banned {
				data, err := os.ReadFile(filepath.Join(e.dir, "metadata.json"))
				if err != nil {
					t.Fatal(err)
				}
				var md metadata
				if err := json.Unmarshal(data, &md); err != nil {
					t.Fatal(err)
				}
				st := md.Pool.States[a.ID]
				if st == nil || !st.HardQuarantined || st.RuleID != "perch-banned" {
					t.Fatalf("not persisted: %+v", st)
				}
				e.srv.markPerchHealthy(a.ID)
				v = e.srv.pool.Views(e.srv.cfg.Get())[a.ID]
				if v.State == nil || !v.State.HardQuarantined || v.State.RuleID != "perch-banned" {
					t.Fatalf("health recovered ban: %+v", v.State)
				}
			}
		})
	}
}
