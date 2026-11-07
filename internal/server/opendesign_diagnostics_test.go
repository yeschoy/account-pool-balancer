package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

func TestOpenDesignRefreshDiagnosticAPI(t *testing.T) {
	var mode atomic.Int32
	var inference atomic.Int32
	blocked := make(chan struct{}, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			inference.Add(1)
			w.WriteHeader(400)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			if mode.Load() == 2 {
				blocked <- struct{}{}
				<-r.Context().Done()
				return
			}
			if mode.Load() == 1 {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"error":{"code":"forbidden_api_key","message":"api key owner banned"}}`)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"mock-model"}]}`)
		case "/api/v1/workspaces/current":
			fmt.Fprint(w, `{"workspaceId":"ws","memberStatus":"active","lifecycleState":"active"}`)
		case "/api/v1/workspaces/ws/billing/preflight":
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"billing_preflight_unavailable"}`)
		case "/api/v1/workspaces/ws/wallet/balance":
			fmt.Fprint(w, `{"balanceUsd":"0"}`)
		case "/api/v1/billing/summary":
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"database_unavailable"}`)
		default:
			t.Errorf("unexpected read %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.stopPerch(); s.logs.Close() }()
	_, err = s.cfg.Update(-1, func(c *config.Config) error {
		c.Quota.Enabled = false
		c.Upstream.RuntimeBase, c.Upstream.ControlBase = up.URL+"/v1", up.URL
		c.Accounts = []config.Account{{ID: "od", Name: "mock OD", Provider: config.ProviderOpenDesign, RuntimeKey: "sk-amr-mock-secret", ControlKey: "vela_ctrl_mock-secret", Enabled: false}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	login := httptest.NewRecorder()
	sess := s.admin.create(login, httptest.NewRequest("GET", "/", nil))
	cookie := login.Result().Cookies()[0]
	path := "/api/accounts/od/refresh-quota"
	post := func(auth, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, nil)
		if auth {
			r.AddCookie(cookie)
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", sess.csrf)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post(false, false); w.Code != 401 {
		t.Fatalf("unauthed diagnostic = %d", w.Code)
	}
	if w := post(true, false); w.Code != 403 {
		t.Fatalf("missing CSRF diagnostic = %d", w.Code)
	}
	initialRevision := s.cfg.Get().Revision
	for _, banned := range []bool{false, true} {
		if banned {
			mode.Store(1)
		}
		w := post(true, true)
		if w.Code != 200 {
			t.Fatalf("completed diagnostic hidden by generic HTTP %d: %s", w.Code, w.Body.String())
		}
		var out struct {
			OK             bool                      `json:"ok"`
			QuotaRefreshed bool                      `json:"quotaRefreshed"`
			Message        string                    `json:"message"`
			Diagnosis      quota.OpenDesignDiagnosis `json:"diagnosis"`
			Checks         quota.OpenDesignState     `json:"checks"`
			NextAt         time.Time                 `json:"nextAt"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.OK || out.QuotaRefreshed || out.Diagnosis.PlanFresh || !out.Diagnosis.WalletFresh || out.Diagnosis.WalletStatus != "empty" || !out.NextAt.After(time.Now()) {
			t.Fatalf("misleading diagnostic: %s", w.Body.String())
		}
		if banned && out.Diagnosis.Status != "banned" {
			t.Fatalf("ban hidden by active workspace / billing outage: %s", w.Body.String())
		}
		if !banned && out.Diagnosis.Status != "upstream-unavailable" {
			t.Fatalf("outage mistaken for ban: %s", w.Body.String())
		}
		for _, secret := range []string{"sk-amr-mock-secret", "vela_ctrl_mock-secret", "query api key by digest"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("diagnostic leaked %q", secret)
			}
		}
		if !strings.Contains(out.Message, "database_unavailable") || strings.Contains(out.Message, "额度已刷新") {
			t.Fatalf("partial result shown as success: %s", out.Message)
		}
	}
	if inference.Load() != 0 || s.cfg.Get().Accounts[0].Enabled || s.cfg.Get().Revision != initialRevision {
		t.Fatal("read-only diagnostic sent inference or changed enablement")
	}
	state, _ := s.pool.ExportState()
	if len(state.States) != 0 {
		t.Fatal("diagnostic altered scheduler state")
	}
	views := s.accountViews(s.cfg.Get())
	if len(views) != 1 || views[0].Diagnosis == nil || views[0].Diagnosis.Status != "banned" || views[0].QuotaFresh || views[0].Diagnosis.AutoRefresh {
		t.Fatalf("account projection disagrees with refresh: %+v", views)
	}
	// Waiting behind another read must not relabel a prior successful check
	// as this button press's completed diagnostic.
	mode.Store(2)
	background, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	done := make(chan struct{})
	go func() { defer close(done); _ = s.quota.RefreshNow(background, s.cfg.Get().Accounts[0]) }()
	<-blocked
	short, stopShort := context.WithTimeout(context.Background(), 30*time.Millisecond)
	r := httptest.NewRequest("POST", path, nil).WithContext(short)
	r.SetPathValue("id", "od")
	w := httptest.NewRecorder()
	s.apiAccountAction(w, r)
	stopShort()
	stopBackground()
	<-done
	if w.Code != 409 || !strings.Contains(w.Body.String(), "本次检测未完成") {
		t.Fatalf("old result passed off as current: %d %s", w.Code, w.Body.String())
	}
	before := s.quota.Get("od")
	_, err = s.cfg.Update(-1, func(c *config.Config) error {
		c.Accounts[0].RuntimeKey = "sk-amr-new-mock"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.quota.Get("od") != nil {
		t.Fatal("replaced runtime key retained old ban")
	}
	// A late old read cannot restore the prior ban after credential changes.
	s.quota.Load(map[string]*quota.Snapshot{"od": before})
	if v := s.accountViews(s.cfg.Get())[0]; v.Quota != nil || v.Diagnosis.Status != "unknown" {
		t.Fatal("old-source diagnostic exposed after credential change")
	}
}

func TestOpenDesignRefreshSuccessAndNoControl(t *testing.T) {
	var posts atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			posts.Add(1)
		}
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"mock"}]}`)
		case "/api/v1/workspaces/current":
			fmt.Fprint(w, `{"workspaceId":"ws"}`)
		case "/api/v1/workspaces/ws/billing/preflight":
			fmt.Fprintf(w, `{"codingPlan":{"eligible":true,"windows":[{"usedCredits":"100","remainingCredits":"0","limitCredits":"100","resetsAt":%q}]}}`, time.Now().Add(time.Hour).Format(time.RFC3339))
		case "/api/v1/workspaces/ws/wallet/balance":
			fmt.Fprint(w, `{"balanceUsd":"2"}`)
		case "/api/v1/billing/summary":
			fmt.Fprint(w, `{"balances":{"subscriptionCredits":"50"}}`)
		default:
			w.WriteHeader(401)
		}
	}))
	defer up.Close()
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.stopPerch(); s.logs.Close() }()
	_, err = s.cfg.Update(-1, func(c *config.Config) error {
		c.Quota.Enabled = false
		c.Upstream.RuntimeBase, c.Upstream.ControlBase = up.URL+"/v1", up.URL
		c.Accounts = []config.Account{{ID: "od", Provider: config.ProviderOpenDesign, RuntimeKey: "sk-amr-mock", ControlKey: "vela_ctrl_mock", Enabled: true}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, withControl := range []bool{true, false} {
		if !withControl {
			_, err = s.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].ControlKey = ""; return nil })
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest("POST", "/api/accounts/od/refresh-quota", nil)
		r.SetPathValue("id", "od")
		w := httptest.NewRecorder()
		s.apiAccountAction(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if out["ok"] != withControl || out["quotaRefreshed"] != withControl {
			t.Fatalf("success/unknown response: %s", w.Body.String())
		}
		if withControl && s.quota.Get("od").OpenDesignDiagnosis(time.Now(), time.Hour).PlanStatus != "exhausted" {
			t.Fatal("fresh measured zero not shown as exhausted")
		}
	}
	if posts.Load() != 0 {
		t.Fatal("sent inference")
	}
	// Other providers retain their pre-existing failure status contract.
	_, err = s.cfg.Update(-1, func(c *config.Config) error {
		c.Accounts = []config.Account{{ID: "modal", Provider: config.ProviderModal, RuntimeKey: "wk-mock.ws-secret", Enabled: true}}
		c.Upstream.ModalBase = up.URL + "/absent"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.quota.RefreshNow(context.Background(), s.cfg.Get().Accounts[0]); err == nil {
		t.Fatal("mock absent catalogue should fail")
	}
	r := httptest.NewRequest("POST", "/api/accounts/modal/refresh-quota", nil)
	r.SetPathValue("id", "modal")
	w := httptest.NewRecorder()
	s.apiAccountAction(w, r)
	if w.Code != 502 {
		t.Fatalf("other provider's contract changed: %d", w.Code)
	}
}
