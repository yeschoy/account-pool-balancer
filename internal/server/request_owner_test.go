package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/pool"
)

const ownerTestBody = `{"model":"m","input":"synthetic owner test","store":false}`

func ownerReply(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"id":"r","object":"response","status":"completed","output":[{"type":"message","id":"msg","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
}
func ownerPost(t *testing.T, e *mistralEnv, token, key, body string) *http.Response {
	t.Helper()
	r, _ := http.NewRequest("POST", e.front.URL+"/v1/responses", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Session-Id", "owner-synthetic-thread")
	if token != "" {
		r.Header.Set(requestOwnerHeader, token)
	}
	response, err := e.front.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func closeOwnerResponse(r *http.Response) { _, _ = io.Copy(io.Discard, r.Body); r.Body.Close() }
func mintOwner(t *testing.T, e *mistralEnv) string {
	t.Helper()
	r := ownerPost(t, e, "start", "mistral-client", ownerTestBody)
	defer closeOwnerResponse(r)
	if r.StatusCode != 200 {
		t.Fatalf("start owner %d", r.StatusCode)
	}
	token := r.Header.Get(requestOwnerHeader)
	if !strings.HasPrefix(token, "ow1.") {
		t.Fatal("missing opaque owner token")
	}
	return token
}

func TestRequestOwnerServerPinBusyScopeAndPrivacy(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(requestOwnerHeader) != "" {
			t.Error("owner token leaked to model")
		}
		ownerReply(w)
	})
	token := mintOwner(t, e)
	cfg := e.s.cfg.Get()
	ck, _ := cfg.ClientKey("client1")
	h := http.Header{}
	h.Set(requestOwnerHeader, token)
	owner, err := e.s.readRequestOwner(cfg, ck, "m", "/v1/responses", h, time.Now())
	if err != nil || owner.claims == nil {
		t.Fatal("cannot validate minted owner")
	}
	id := owner.claims.Account
	for _, caseData := range []struct{ token, key, body string }{{token + "x", "mistral-client", ownerTestBody}, {token, "mistral-other-client", ownerTestBody}, {token, "mistral-client", strings.Replace(ownerTestBody, `"model":"m"`, `"model":"other"`, 1)}} {
		response := ownerPost(t, e, caseData.token, caseData.key, caseData.body)
		if response.StatusCode != 409 {
			t.Fatalf("scope accepted: %d", response.StatusCode)
		}
		closeOwnerResponse(response)
	}
	if e.count() != 1 {
		t.Fatal("invalid token reached model")
	}
	lease, err := e.s.pool.Acquire(context.Background(), cfg.ForProvider(config.ProviderMistral), pool.Request{PinnedAccountID: id, Group: config.MistralGroup, Model: "m", RequestID: "busy-test"})
	if err != nil {
		t.Fatal(err)
	}
	response := ownerPost(t, e, token, "mistral-client", ownerTestBody)
	if response.StatusCode != 429 {
		t.Fatalf("busy owner overflowed: %d", response.StatusCode)
	}
	closeOwnerResponse(response)
	lease.Release()
	if e.count() != 1 {
		t.Fatal("busy owner used another account")
	}
	response = ownerPost(t, e, token, "mistral-client", ownerTestBody)
	if response.StatusCode != 200 || response.Header.Get(requestOwnerHeader) != token {
		t.Fatal("owner changed or was renewed")
	}
	closeOwnerResponse(response)
	if e.count() != 2 || e.call(0).key != e.call(1).key {
		t.Fatal("continuation changed owner")
	}
	for _, kind := range []string{"requests", "errors", "details"} {
		page, _ := e.s.logs.Query(kind, nil, time.Time{}, time.Time{}, 100, 0)
		for _, raw := range page.Items {
			if strings.Contains(string(raw), token) {
				t.Fatalf("owner token leaked into %s", kind)
			}
		}
	}
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == id {
				c.Accounts[i].RuntimeKey = "mstrl_rotated_owner"
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	response = ownerPost(t, e, token, "mistral-client", ownerTestBody)
	if response.StatusCode != 409 {
		t.Fatal("changed source revived owner")
	}
	closeOwnerResponse(response)
	if e.count() != 2 {
		t.Fatal("changed source sent inference")
	}
}
func TestRequestOwnerExpiryNoRetryAndCapabilities(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) { ownerReply(w) })
	req, _ := http.NewRequest("GET", e.front.URL+"/v1/pool-capabilities", nil)
	req.Header.Set("Authorization", "Bearer mistral-client")
	resp, err := e.front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var cap map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&cap)
	resp.Body.Close()
	if resp.StatusCode != 200 || cap["requestOwner"] != "v1" || e.count() != 0 {
		t.Fatal("capability probe used model")
	}
	cfg := e.s.cfg.Get()
	ck, _ := cfg.ClientKey("client1")
	a, _ := cfg.Account("m1")
	expired, err := e.s.ownerForLease(cfg, cfg, ck, "m", a, requestOwner{enabled: true}, time.Now().Add(-10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	resp = ownerPost(t, e, expired, "mistral-client", ownerTestBody)
	if resp.StatusCode != 409 {
		t.Fatal("expired token accepted")
	}
	closeOwnerResponse(resp)
	if e.count() != 0 {
		t.Fatal("expired token sent inference")
	}
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"message":"synthetic failure"}}`)
	})
	resp = ownerPost(t, e, "start", "mistral-client", ownerTestBody)
	closeOwnerResponse(resp)
	if e.count() != 1 {
		t.Fatal("owner start failure rotated accounts")
	}
}
