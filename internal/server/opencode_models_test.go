package server

import (
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
	"account-pool-balancer/internal/quota"
)

func TestOpenCodeAdvertisedModelIsNotAutomaticallyRoutable(t *testing.T) {
	e := newOCEnv(t, false)
	snap := e.s.quota.Get("zen1")
	snap.Catalog["unknown-sdk"] = true
	snap.OpenCode.Models["unknown-sdk"] = opencode.Model{}
	e.s.quota.Load(map[string]*quota.Snapshot{"zen1": snap})
	for _, a := range e.s.accountViews(e.s.cfg.Get()) {
		if a.ID == "zen1" && (a.RoutableModels == nil || *a.RoutableModels != 4 || len(a.Quota.Catalog) != 5) {
			t.Fatal("advertised count presented as full protocol support")
		}
	}
	models := e.s.openCodeGroupModels(e.s.cfg.Get(), config.ProviderOpenCodeZen)
	if len(models) != 4 {
		t.Fatalf("unknown SDK advertised downstream: %d", len(models))
	}
	for _, v := range models {
		m := v.(map[string]any)
		if m["id"] == "unknown-sdk" {
			t.Fatal("unknown protocol silently treated as Chat")
		}
		if _, ok := m["context_window"]; ok {
			t.Fatal("unknown context window advertised as zero")
		}
	}
}
