package config

import (
	"encoding/json"
	"testing"
)

func TestRecentRequestsStrategyPreservesOtherPoolDefaults(t *testing.T) {
	c := Default()
	r := c.Routing
	r.Strategy = "least-requests-1h"
	c.OpenCode.GoRouting = &r
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var restored Config
	if err = json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if err = restored.Normalize(); err != nil {
		t.Fatal(err)
	}
	if restored.ForProvider(ProviderOpenCodeGo).Routing.Strategy != "least-requests-1h" || restored.Routing.Strategy != "smart" || restored.ForProvider(ProviderOpenCodeZen).Routing.Strategy != "smart" || restored.ForProvider(ProviderTokenHarbor).Routing.Strategy != "smart" {
		t.Fatal("optional Go strategy leaked or failed round-trip")
	}
}
