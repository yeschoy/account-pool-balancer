package quota

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestOpenCodeLateCredentialReadCannotRestoreOldPermissions(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/v1/models":
			id := "new-model"
			if strings.Contains(key, "old-key") {
				id = "old-model"
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, id)
		case "/cap":
			fmt.Fprint(w, `{"opencode-go":{"npm":"@ai-sdk/openai-compatible","models":{"old-model":{},"new-model":{}}}}`)
		case "/v1/usage":
			fmt.Fprint(w, `{"usage":{"rolling":{"status":"ok","percent":5,"resetsAt":"2099-01-01T00:00:00Z"},"weekly":{"status":"ok","percent":10,"resetsAt":"2099-02-01T00:00:00Z"},"monthly":{"status":"ok","percent":20,"resetsAt":"2099-03-01T00:00:00Z"}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	c := config.Default()
	c.OpenCode.Enabled = true
	c.OpenCode.GoBase = up.URL + "/v1"
	c.OpenCode.CapabilitiesURL = up.URL + "/cap"
	c.Accounts = []config.Account{{ID: "a", Provider: config.ProviderOpenCodeGo, RuntimeKey: "old-key-123", Enabled: true}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[config.Config]
	current.Store(c)
	p := NewPoller(current.Load, func(string) *http.Client { return up.Client() })
	var updates atomic.Int32
	p.OnUpdate = func(string) { updates.Add(1) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- p.RefreshNow(ctx, c.Accounts[0]) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("old read not started")
	}
	next := c.Clone()
	next.Revision++
	next.Accounts[0].RuntimeKey = "new-key-123"
	current.Store(next)
	p.Forget("a")
	once.Do(func() { close(release) })
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if p.View("a") != nil || updates.Load() != 0 {
		t.Fatal("late old-key data restored permissions")
	}
	if err := p.RefreshNow(ctx, next.Accounts[0]); err != nil {
		t.Fatal(err)
	}
	snap := p.Get("a")
	if snap == nil || !snap.Catalog["new-model"] || snap.Catalog["old-model"] || snap.OpenCode.Source != OpenCodeSource(next, next.Accounts[0]) {
		t.Fatal("new key polluted by old catalog")
	}
	all := p.All()
	all["a"].Catalog["fake"] = true
	delete(all["a"].OpenCode.Models, "new-model")
	if p.View("a").Catalog["fake"] || p.View("a").OpenCode.Models["new-model"].Protocol == "" {
		t.Fatal("reader mutation changed immutable snapshot")
	}
}
