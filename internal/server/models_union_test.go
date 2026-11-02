package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"account-pool-balancer/internal/config"
)

// Each Modal proxy token reaches its own Endpoints, so the pool's model list
// must be the union of every account's catalogue: publishing one account's
// list hides models the other accounts can serve, and a relay such as new-api
// never offers them. Routing still goes per account (see the pool's
// not-entitled test).
func TestModelsListsUnionOfAccounts(t *testing.T) {
	cat := map[string][]string{
		"wk-a.ws-1": {"kimi-k3", "deepseek-v4-1-flash-1017"},
		"wk-b.ws-2": {"glm-5-3-flash", "deepseek-v4-1-flash-1017"},
		"wk-c.ws-3": {"qwen3-8-max-vl-thinking"},
	}
	e := newEnv(t, []config.Account{
		{ID: "a1", Provider: config.ProviderModal, RuntimeKey: "wk-a.ws-1", Group: "modal"},
		{ID: "a2", Provider: config.ProviderModal, RuntimeKey: "wk-b.ws-2", Group: "modal"},
		{ID: "a3", Provider: config.ProviderModal, RuntimeKey: "wk-c.ws-3", Group: "modal"},
	}, func(c *config.Config) {
		c.ClientKeys = []config.ClientKey{{ID: "ck1", Group: "modal", KeyHash: config.HashKey("sk-odb-test-client-key-0001"), KeyPrefix: "x", Enabled: true}}
		// the union is what this test is about: don't let the per-key
		// catalogue filter narrow it back down here
		c.Routing.IgnoreCatalog = true
		// Modal reads its catalogue at Upstream.ModalBase, not RuntimeBase
		c.Upstream.ModalBase = c.Upstream.RuntimeBase
	})
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, _ map[string]any) {
		if r.URL.Path != "/v1/models" {
			writeOK(w, key)
			return
		}
		data := make([]map[string]any, 0, 3)
		for _, id := range cat[key] {
			data = append(data, map[string]any{"id": id, "object": "model", "enabled": true})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}

	req, _ := http.NewRequest(http.MethodGet, e.front.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+e.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range doc.Data {
		got[m.ID] = true
	}
	for _, want := range []string{"kimi-k3", "glm-5-3-flash", "qwen3-8-max-vl-thinking", "deepseek-v4-1-flash-1017"} {
		if !got[want] {
			t.Fatalf("model %q missing from the pool's list: %v", want, doc.Data)
		}
	}
	if len(got) != 4 {
		t.Fatalf("union must dedupe the shared model: %v", got)
	}
}
