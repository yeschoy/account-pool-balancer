package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"account-pool-balancer/internal/config"
)

// Two Modal proxy tokens, each with its own hostname for the same base
// model. The pool must publish the base model once and route a request for
// it to either account, translating the model to that account's hostname.
func TestBaseModelCollapsesAndRoutes(t *testing.T) {
	host := map[string]string{
		"wk-a.ws-1": "tokenA--ep-deepseek-v4-1-flash-1017-server.us-west.modal.direct",
		"wk-b.ws-2": "tokenB--ep-deepseek-v4-1-flash-1017-server.us-west.modal.direct",
	}
	const base = "deepseek-ai/DeepSeek-V4.1-Flash-1017"
	const public = "deepseek-v4-1-flash-1017"
	var mu sync.Mutex
	gotModels := map[string][]string{}

	e := newEnv(t, []config.Account{
		{ID: "a1", Provider: config.ProviderModal, RuntimeKey: "wk-a.ws-1", Group: "modal"},
		{ID: "a2", Provider: config.ProviderModal, RuntimeKey: "wk-b.ws-2", Group: "modal"},
	}, func(c *config.Config) {
		c.ClientKeys = []config.ClientKey{{ID: "ck1", Group: "modal", KeyHash: config.HashKey("sk-odb-test-client-key-0001"), KeyPrefix: "x", Enabled: true}}
		c.Routing.IgnoreCatalog = false
		c.Upstream.ModalBase = c.Upstream.RuntimeBase
	})
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{
				{"id": host[key], "object": "model", "base_model_id": base, "name": "DeepSeek V4.1 Flash 1017"},
			}})
			return
		}
		mu.Lock()
		gotModels[key] = append(gotModels[key], body["model"].(string))
		mu.Unlock()
		writeOK(w, "sk-amr-padding-padding")
	}

	// populate both accounts' catalogues
	for _, id := range []string{"a1", "a2"} {
		a, _ := e.srv.cfg.Get().Account(id)
		if err := e.srv.quota.RefreshNow(e.t.Context(), a); err != nil {
			t.Fatalf("refresh %s: %v", id, err)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, e.front.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+e.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Data) != 1 || doc.Data[0].ID != public {
		t.Fatalf("two tokens serving one base model must publish it once, got %+v", doc.Data)
	}

	// a request naming the base model reaches both accounts, each asked for
	// its own hostname
	for i := 0; i < 6; i++ {
		r2 := e.post("/v1/chat/completions", map[string]any{
			"model": public, "messages": []any{map[string]any{"role": "user", "content": "u" + string(rune('a'+i))}},
		}, nil)
		r2.Body.Close()
		if r2.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d", i, r2.StatusCode)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for key, models := range gotModels {
		for _, m := range models {
			if m != host[key] {
				t.Fatalf("%s was asked for %q, want its own hostname %q", key, m, host[key])
			}
		}
	}
	if len(gotModels) != 2 {
		t.Fatalf("both accounts must serve the shared base model, got %v", gotModels)
	}
}

func TestModalModelsListsFiveShortNames(t *testing.T) {
	models := []struct{ name, base, endpoint string }{
		{"glm-5-3", "zai-org/GLM-5.3", "glm-5-3"},
		{"deepseek-v4-1-flash-1017", "deepseek-ai/DeepSeek-V4.1-Flash-1017", "deepseek-v4-1-flash-1017"},
		{"kimi-k3", "moonshotai/Kimi-K3", "kimi-k3"},
		{"glm-5-3-flash", "zai-org/GLM-5.3-Flash", "glm-5-3-flash"},
		{"qwen3-8-max", "Qwen/Qwen3.8-Max-VL-Thinking", "qwen3-8-max-vl-thinking"},
	}
	e := newEnv(t, []config.Account{
		{ID: "a1", Provider: config.ProviderModal, RuntimeKey: "wk-a.ws-1", Group: "modal"},
		{ID: "a2", Provider: config.ProviderModal, RuntimeKey: "wk-b.ws-2", Group: "modal"},
	}, func(c *config.Config) {
		c.ClientKeys = []config.ClientKey{{ID: "ck1", Group: "modal", KeyHash: config.HashKey("sk-odb-test-client-key-0001"), KeyPrefix: "x", Enabled: true}}
		c.Upstream.ModalBase = c.Upstream.RuntimeBase
		c.Routing.IgnoreCatalog = false
	})
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
		prefix := "tokenA"
		if key == "wk-b.ws-2" {
			prefix = "tokenB"
		}
		if r.URL.Path == "/v1/models" {
			var data []map[string]any
			for _, m := range models {
				data = append(data, map[string]any{
					"id": prefix + "--ep-" + m.endpoint + "-server.us-west.modal.direct", "base_model_id": m.base,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
			return
		}
		if model, _ := body["model"].(string); !strings.HasPrefix(model, prefix+"--ep-") || !strings.HasSuffix(model, ".us-west.modal.direct") {
			t.Errorf("account %s received untranslated model %q", key, model)
		}
		writeOK(w, "sk-amr-padding-padding")
	}
	for _, a := range e.srv.cfg.Get().Accounts {
		if err := e.srv.quota.RefreshNow(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, e.front.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+e.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct{ Data []struct{ ID string } }
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(doc.Data) != len(models) {
		t.Fatalf("status %d, models = %+v", resp.StatusCode, doc.Data)
	}
	seen := map[string]bool{}
	for _, model := range doc.Data {
		seen[model.ID] = true
	}
	for _, model := range models {
		if !seen[model.name] {
			t.Fatalf("short model %q missing: %v", model.name, seen)
		}
		resp := e.post("/v1/chat/completions", map[string]any{
			"model": model.name, "messages": []any{map[string]any{"role": "user", "content": "OK"}},
		}, nil)
		body := readAll(resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("model %s: status %d, body %s", model.name, resp.StatusCode, body)
		}
	}
}
