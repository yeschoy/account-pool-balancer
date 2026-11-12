package quota

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
)

func TestDeployCatalogFailureAndPersistence(t *testing.T) {
	var status atomic.Int32
	status.Store(200)
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/models" || r.Method != "GET" {
			t.Error("unexpected request", r.Method, r.URL.Path)
		}
		w.WriteHeader(int(status.Load()))
		if status.Load() == 200 {
			fmt.Fprint(w, `{"data":[{"id":"native","base_model_id":"foreign-alias"}]}`)
		} else {
			fmt.Fprint(w, `{"message":"PRIVATE_REFLECTION"}`)
		}
	}))
	defer up.Close()
	c := config.Default()
	c.Deploy.BaseURL = up.URL + "/api/v1"
	c.Accounts = []config.Account{{ID: "d", Provider: config.ProviderDeploy, RuntimeKey: "opaque_deploy_secret"}}
	p := NewPoller(func() *config.Config { return c }, func(string) *http.Client { return up.Client() })
	a := c.Accounts[0]
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	first := p.Get(a.ID)
	if first.CatalogSource != ResponseBindingSource(c, a) || !first.Catalog["native"] || len(first.Aliases) != 0 || first.WalletUSD != "" || !first.PlanAt.IsZero() {
		t.Fatal("wrong source/alias/fake balance")
	}
	if d := first.DeployDiagnosis(time.Now(), time.Minute); !d.CatalogFresh || d.QuotaState != "not-connected" || d.QuotaReason == "" {
		t.Fatal("diagnosis missing")
	}
	b, _ := json.Marshal(first)
	var restored Snapshot
	if json.Unmarshal(b, &restored) != nil || !restored.Catalog["native"] {
		t.Fatal("snapshot roundtrip")
	}
	for _, st := range []int32{401, 403, 429, 500} {
		status.Store(st)
		if p.RefreshNow(context.Background(), a) == nil {
			t.Fatal("refresh hid failure", st)
		}
		s := p.Get(a.ID)
		if !s.CatalogAt.Equal(first.CatalogAt) || !s.Catalog["native"] || s.DeployDiagnosis(time.Now(), time.Minute).CatalogFresh {
			t.Fatal("failure erased history or reported fresh")
		}
		if strings.Contains(s.CatalogError, "PRIVATE_REFLECTION") {
			t.Fatal("raw upstream error leaked")
		}
	}
	c.Deploy.ModelSyncSec = 0
	before := calls.Load()
	if err := p.fetchDeploy(context.Background(), c, a, false); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("sync=0 still periodically fetched")
	}
	status.Store(200)
	if p.RefreshNow(context.Background(), a) != nil || calls.Load() != before+1 {
		t.Fatal("manual refresh did not force read")
	}
}

func TestDeployDiscardsLateSourceAndRedirect(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		fmt.Fprint(w, `{"data":[{"id":"bad"}]}`)
	}))
	defer target.Close()
	var mode atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() {
			http.Redirect(w, r, target.URL, 302)
			return
		}
		close(started)
		<-release
		fmt.Fprint(w, `{"data":[{"id":"old-source"}]}`)
	}))
	defer up.Close()
	c := config.Default()
	c.Deploy.BaseURL = up.URL + "/api/v1"
	c.Accounts = []config.Account{{ID: "d", Provider: config.ProviderDeploy, RuntimeKey: "opaque_deploy_secret"}}
	var current atomic.Pointer[config.Config]
	current.Store(c)
	p := NewPoller(current.Load, func(string) *http.Client { return up.Client() })
	done := make(chan error, 1)
	go func() { done <- p.RefreshNow(context.Background(), c.Accounts[0]) }()
	<-started
	next := c.Clone()
	next.Accounts[0].RuntimeKey = "changed_deploy_secret"
	current.Store(next)
	close(release)
	<-done
	if p.Get("d") != nil {
		t.Fatal("late source revived old catalog")
	}
	mode.Store(true)
	if p.RefreshNow(context.Background(), next.Accounts[0]) == nil {
		t.Fatal("redirect accepted")
	}
	if redirected.Load() != 0 {
		t.Fatal("credential redirect followed")
	}
}
