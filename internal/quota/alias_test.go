package quota

import (
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func cat(models map[string]CatalogModel) *Catalog {
	return &Catalog{Models: models}
}

// Modal's Shared Endpoints are hostnames unique per proxy token, each
// carrying the base model it serves. Three tokens running DeepSeek must
// publish one name between them, and each must be asked for its own
// hostname.
func TestCatalogAliasesCollapseAndTranslate(t *testing.T) {
	now := time.Now()
	a, b := &Snapshot{}, &Snapshot{}
	applyCatalog(a, cat(map[string]CatalogModel{
		"tokenA--ep-deepseek-v4-1-flash-1017-server.us-west.modal.direct": {Enabled: true, Base: "deepseek-ai/DeepSeek-V4.1-Flash-1017"},
		"tokenA--ep-glm-5-3-flash-server.us-west.modal.direct":            {Enabled: true, Base: "zai-org/GLM-5.3-Flash"},
	}), now)
	applyCatalog(b, cat(map[string]CatalogModel{
		"tokenB--ep-deepseek-v4-1-flash-1017-server.us-west.modal.direct": {Enabled: true, Base: "deepseek-ai/DeepSeek-V4.1-Flash-1017"},
	}), now)

	if got := a.PublicModels(now); len(got) != 2 {
		t.Fatalf("public models = %v", got)
	}
	pub := map[string]bool{}
	for _, m := range a.PublicModels(now) {
		pub[m] = true
	}
	if !pub["deepseek-v4-1-flash-1017"] {
		t.Fatalf("base model must be published, got %v", pub)
	}
	// the two accounts publish the same name for their own hostnames
	if got := b.PublicModels(now); len(got) != 1 || got[0] != "deepseek-v4-1-flash-1017" {
		t.Fatalf("second account = %v", got)
	}
	// and each is asked for its own hostname
	if got := a.UpstreamModel("deepseek-ai/DeepSeek-V4.1-Flash-1017"); got != "tokenA--ep-deepseek-v4-1-flash-1017-server.us-west.modal.direct" {
		t.Fatalf("a translated to %q", got)
	}
	if got := b.UpstreamModel("deepseek-ai/DeepSeek-V4.1-Flash-1017"); got != "tokenB--ep-deepseek-v4-1-flash-1017-server.us-west.modal.direct" {
		t.Fatalf("b translated to %q", got)
	}
	// a hostname still names its own account, so targeting one stays possible
	if got := a.UpstreamModel("tokenA--ep-glm-5-3-flash-server.us-west.modal.direct"); got != "tokenA--ep-glm-5-3-flash-server.us-west.modal.direct" {
		t.Fatalf("explicit hostname must pass through, got %q", got)
	}
	// an account that doesn't serve the model gets no translation, and is
	// then excluded by NotEntitled
	if got := a.UpstreamModel("qwen3-8-max-vl-thinking"); got != "qwen3-8-max-vl-thinking" {
		t.Fatalf("unknown model must pass through, got %q", got)
	}
	if !a.NotEntitled("tokenB--ep-deepseek-v4-1-flash-1017-server.us-west.modal.direct", now) {
		t.Fatal("another token's hostname must not be entitled here")
	}
	if a.NotEntitled("deepseek-ai/DeepSeek-V4.1-Flash-1017", now) {
		t.Fatal("the base model is served through an alias and must be entitled")
	}
}

func TestModalShortNamesFromExistingCatalog(t *testing.T) {
	models := []struct {
		name, base, endpoint string
	}{
		{"glm-5-3", "zai-org/GLM-5.3", "glm-5-3"},
		{"deepseek-v4-1-flash-1017", "deepseek-ai/DeepSeek-V4.1-Flash-1017", "deepseek-v4-1-flash-1017"},
		{"kimi-k3", "moonshotai/Kimi-K3", "kimi-k3"},
		{"glm-5-3-flash", "zai-org/GLM-5.3-Flash", "glm-5-3-flash"},
		{"qwen3-8-max", "Qwen/Qwen3.8-Max-VL-Thinking", "qwen3-8-max-vl-thinking"},
	}
	for _, model := range models {
		t.Run(model.name, func(t *testing.T) {
			now := time.Now()
			host := "tokenA--ep-" + model.endpoint + "-server.us-west.modal.direct"
			// A snapshot saved before short names existed must work immediately.
			s := &Snapshot{
				Catalog: map[string]bool{host: true}, CatalogAt: now,
				Bases: map[string]string{host: model.base}, Aliases: map[string]string{model.base: host},
			}
			if got := s.PublicModels(now); len(got) != 1 || got[0] != model.name {
				t.Fatalf("public models = %v, want [%s]", got, model.name)
			}
			for _, name := range []string{model.name, model.base, host} {
				if got := s.UpstreamModel(name); got != host {
					t.Fatalf("upstream model for %q = %q, want %q", name, got, host)
				}
				if s.NotEntitled(name, now) {
					t.Fatalf("account must serve %q", name)
				}
			}
		})
	}
}

// A catalogue without base_model_id (every pool but Modal) must behave
// exactly as before: ids are the models, nothing is translated.
func TestCatalogWithoutBasesIsUnchanged(t *testing.T) {
	now := time.Now()
	s := &Snapshot{}
	applyCatalog(s, cat(map[string]CatalogModel{
		"claude-sonnet-5": {Enabled: true},
		"gpt-6-sol":       {Enabled: true},
	}), now)
	if s.Aliases != nil || s.Bases != nil {
		t.Fatalf("no aliases expected: %v %v", s.Aliases, s.Bases)
	}
	if got := s.UpstreamModel("claude-sonnet-5"); got != "claude-sonnet-5" {
		t.Fatalf("got %q", got)
	}
	if len(s.PublicModels(now)) != 2 {
		t.Fatalf("public models = %v", s.PublicModels(now))
	}
}

// A catalogue written by a build without alias tables has models but no
// Bases; it must be read again rather than served as is, or an upgraded
// instance keeps publishing raw endpoint hostnames for up to catalogTTL.
func TestCatalogDueRefreshesAliaslessSnapshot(t *testing.T) {
	now := time.Now()
	fresh := &Snapshot{Catalog: map[string]bool{"a--ep-x": true}, CatalogAt: now.Add(-time.Minute),
		Bases: map[string]string{"a--ep-x": "base"}}
	if catalogDue(fresh, now, true, true) {
		t.Fatal("a fresh catalogue that has aliases is not due")
	}
	// fresh in time terms, but carrying no alias tables: due anyway
	stale := &Snapshot{Catalog: map[string]bool{"a--ep-x": true}, CatalogAt: now}
	if !catalogDue(stale, now, true, true) {
		t.Fatal("an alias-less catalogue must be re-read")
	}
	// after the aliases are there, the plain TTL rule applies again
	stale.Bases = map[string]string{"a--ep-x": "base"}
	if catalogDue(stale, now, true, true) {
		t.Fatal("a snapshot with aliases must not be re-read inside the TTL")
	}
	if !catalogDue(stale, now.Add(catalogTTL), true, true) {
		t.Fatal("the TTL still applies")
	}
	// no runtime key: nothing to read
	if catalogDue(stale, now.Add(time.Hour), false, true) {
		t.Fatal("an account without a key is never read")
	}
}

// A pool whose upstream never publishes base_model_id (OpenDesign, River)
// must keep the plain TTL rule: re-reading on every tick would cost a
// /v1/models call per account per poll.
func TestCatalogDueIgnoresAliaslessForNonAliasingPools(t *testing.T) {
	now := time.Now()
	s := &Snapshot{Catalog: map[string]bool{"claude-sonnet-5": true}, CatalogAt: now}
	if catalogDue(s, now, true, false) {
		t.Fatal("a pool that never publishes aliases must not be re-read")
	}
}

// A poll that finds the catalogue not yet due must schedule the next one at
// the moment it falls due, not a whole catalogTTL later: otherwise the
// catalogue is read every other tick and sits older than staleSec, which the
// console shows as 数据过期.
func TestNextPollLandsOnDueTime(t *testing.T) {
	now := time.Now()
	cfg := &config.Config{Quota: config.Quota{IntervalSec: 60}}

	// read 3 minutes ago: due in 7 minutes, which is inside the 10m default
	s := &Snapshot{CatalogAt: now.Add(-3 * time.Minute)}
	got := nextPoll(s, cfg, now, true)
	want := now.Add(7 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("next poll = %v, want %v (the due time)", got.Sub(now), want.Sub(now))
	}
	// read 9 minutes ago: due in 1 minute, well before the default interval
	s = &Snapshot{CatalogAt: now.Add(-9 * time.Minute)}
	if got := nextPoll(s, cfg, now, true); !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("next poll = %v, want +1m", got.Sub(now))
	}
	// never read: nothing to line up with, keep the default
	s = &Snapshot{}
	if got := nextPoll(s, cfg, now, true); !got.Equal(now.Add(catalogTTL)) {
		t.Fatalf("next poll = %v, want the default TTL", got.Sub(now))
	}
	// no key: keep the default
	s = &Snapshot{CatalogAt: now}
	if got := nextPoll(s, cfg, now, false); !got.Equal(now.Add(catalogTTL)) {
		t.Fatalf("next poll = %v, want the default TTL", got.Sub(now))
	}
}
