package server

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func cacheAuditForTest(t *testing.T) *mistralCacheAudit {
	t.Helper()
	a, err := newMistralCacheAudit(&config.Config{ClientKeys: []config.ClientKey{{ID: "client1", Provider: config.ProviderMistral, Enabled: true}}}, "client1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func cacheAuditRecordForTest(id string) *requestLog {
	return &requestLog{RequestID: id, Provider: config.ProviderMistral, ClientKeyID: "client1", Model: "m", Path: "/v1/chat/completions"}
}
func cacheAuditBodyForTest(messages, tools string) []byte {
	return []byte(`{"model":"m","messages":` + messages + `,"tools":` + tools + `,"prompt_cache_key":"private-cache"}`)
}

func TestMistralCacheAuditFingerprintsAndOrder(t *testing.T) {
	a := cacheAuditForTest(t)
	const messages = `[{"role":"system","content":"private-prefix"},{"role":"user","content":"question","n":9007199254740993}]`
	const tools = `[{"type":"function","function":{"name":"z","parameters":{"properties":{"z":{"type":"string"},"a":{"type":"number"}}}}},{"type":"function","function":{"name":"a"}}]`
	body := cacheAuditBodyForTest(messages, tools)
	original := string(body)
	base := a.fingerprint(body, "messages", nil)
	if base.view.Structure != "complete" || base.view.SequenceCount != 2 || base.view.ToolsCount != 2 || !base.view.SequenceComplete {
		t.Fatalf("bad structure: %+v", base.view)
	}
	if a.fingerprint(body, "messages", base) != base {
		t.Fatal("identical payload should reuse immutable fingerprint")
	}
	grown := a.fingerprint(cacheAuditBodyForTest(strings.TrimSuffix(messages, "]")+`,{"role":"assistant","content":"answer"}]`, tools), "messages", nil)
	cmp := compareCacheFingerprints(base, grown)
	if cmp.BodyEqual || cmp.PreviousSequenceRetained == nil || !*cmp.PreviousSequenceRetained || *cmp.CommonSequencePrefix != 2 || !*cmp.ToolsEqual || !*cmp.OtherFieldsEqual {
		t.Fatalf("growing history misclassified: %+v", cmp)
	}
	var ms, ts []json.RawMessage
	if json.Unmarshal([]byte(messages), &ms) != nil || json.Unmarshal([]byte(tools), &ts) != nil {
		t.Fatal("fixture")
	}
	swappedMessages := fmt.Sprintf("[%s,%s]", ms[1], ms[0])
	swappedTools := fmt.Sprintf("[%s,%s]", ts[1], ts[0])
	cases := []struct {
		name                    string
		body                    []byte
		messageOrder, toolOrder bool
	}{
		{"message permutation", cacheAuditBodyForTest(swappedMessages, tools), true, false},
		{"tool permutation", cacheAuditBodyForTest(messages, swappedTools), false, true},
		{"schema property order", cacheAuditBodyForTest(messages, strings.Replace(tools, `"z":{"type":"string"},"a":{"type":"number"}`, `"a":{"type":"number"},"z":{"type":"string"}`, 1)), false, false},
		{"number precision", cacheAuditBodyForTest(strings.Replace(messages, "9007199254740993", "9007199254740992", 1), tools), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := compareCacheFingerprints(base, a.fingerprint(tc.body, "messages", nil))
			if c.BodyEqual || c.SequenceReordered == nil || *c.SequenceReordered != tc.messageOrder || c.ToolsReordered == nil || *c.ToolsReordered != tc.toolOrder {
				t.Fatalf("incorrect permutation classification: %+v", c)
			}
			if tc.name == "schema property order" && *c.ToolsEqual {
				t.Fatal("schema wire order was ignored")
			}
		})
	}
	if string(body) != original {
		t.Fatal("diagnostics mutated input")
	}
	serial, _ := json.Marshal(base.view)
	for _, secret := range []string{"private-prefix", "question", "private-cache", "9007199254740993", `"properties"`} {
		if strings.Contains(string(serial), secret) {
			t.Fatalf("fingerprint leaked %q", secret)
		}
	}
	if a.tag("x", []byte("ab"), []byte("c")) == a.tag("x", []byte("a"), []byte("bc")) || a.tag("x", body) == a.tag("y", body) {
		t.Fatal("missing HMAC domain/framing isolation")
	}
	if cacheAuditForTest(t).fingerprint(body, "messages", nil).view.Tag == base.view.Tag {
		t.Fatal("process HMAC keys must differ")
	}
}

func TestMistralCacheAuditFullMiddleAndBounds(t *testing.T) {
	a := cacheAuditForTest(t)
	payload := strings.Repeat("stable-private-body-", 360000) // >6 MiB; do not test just a log preview.
	encoded, _ := json.Marshal(payload)
	body := cacheAuditBodyForTest(`[{"role":"user","content":`+string(encoded)+`}]`, `[]`)
	changed := append([]byte(nil), body...)
	changed[len(changed)/2] = 'X'
	left, right := a.fingerprint(body, "messages", nil), a.fingerprint(changed, "messages", nil)
	if len(body) < 6<<20 || left.view.Tag == right.view.Tag || *compareCacheFingerprints(left, right).CommonSequencePrefix != 0 {
		t.Fatal("complete middle was not fingerprinted")
	}
	many := `[` + strings.Repeat(`{"role":"user","content":"x"},`, mistralAuditMessages) + `{"role":"user","content":"last"}]`
	limited := a.fingerprint(cacheAuditBodyForTest(many, `[]`), "messages", nil)
	if limited.view.SequenceCount != mistralAuditMessages+1 || limited.view.SequenceComplete || len(limited.messages) != mistralAuditMessages || compareCacheFingerprints(limited, limited).PreviousSequenceRetained != nil {
		t.Fatal("capped message comparison pretended to be complete")
	}
	toolLimited := a.fingerprint(cacheAuditBodyForTest(`[]`, `[`+strings.Repeat(`{"type":"function"},`, mistralAuditTools)+`{}]`), "messages", nil)
	if toolLimited.view.ToolsComplete || toolLimited.view.ToolsCount != mistralAuditTools+1 || compareCacheFingerprints(toolLimited, toolLimited).ToolsReordered != nil {
		t.Fatal("capped tool comparison pretended to be complete")
	}
	for _, body := range []string{`{`, `[]`, `{"messages":[],"messages":[]}`, `{"messages":[],"\u0074ools":[],"tools":[]}`} {
		if s := a.fingerprint([]byte(body), "messages", nil); s.view.Structure == "complete" || compareCacheFingerprints(s, s).ToolsEqual != nil {
			t.Fatalf("invalid/ambiguous structure accepted: %s", body)
		}
	}
	fields := `{`
	for i := 0; i < mistralAuditFields+1; i++ {
		if i > 0 {
			fields += ","
		}
		fields += fmt.Sprintf(`"field%d":0`, i)
	}
	fields += "}"
	if a.fingerprint([]byte(fields), "messages", nil).view.Structure != "field-limit" {
		t.Fatal("root field limit missing")
	}
	response := a.fingerprint([]byte(`{"input":"native text","tools":[]}`), "input", nil)
	if response.view.SequenceComplete || response.view.SequenceTag == "" || response.view.ToolsTag == "" {
		t.Fatal("non-array native Responses must retain full tag without invented item comparisons")
	}
}

func TestMistralCacheAuditStartupOptions(t *testing.T) {
	dir := t.TempDir()
	store, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(-1, func(c *config.Config) error {
		c.ClientKeys = []config.ClientKey{{ID: "startup-audit", Group: config.MistralGroup, Provider: config.ProviderMistral, Enabled: true, KeyHash: config.HashKey("local-fake-key")}}
		c.Quota.Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir, Options{MistralCacheAuditKeyID: "startup-audit"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.stopPerch(); s.logs.Close() }()
	if s.mistralAudit == nil || s.mistralAudit.keyID != "startup-audit" {
		t.Fatal("startup option not wired")
	}
	if _, err := New(t.TempDir(), Options{MistralCacheAuditKeyID: "missing"}); err == nil {
		t.Fatal("invalid startup option silently ignored")
	}
}

func TestMistralCacheAuditOptInScopeAndBudget(t *testing.T) {
	for _, id := range []string{"missing", "disabled", "other"} {
		cfg := &config.Config{ClientKeys: []config.ClientKey{{ID: "disabled", Provider: config.ProviderMistral}, {ID: "other", Provider: config.ProviderTokenHarbor, Enabled: true}}}
		if _, err := newMistralCacheAudit(cfg, id, time.Now()); err == nil {
			t.Fatalf("invalid target %s accepted", id)
		}
	}
	if a, err := newMistralCacheAudit(nil, "", time.Now()); a != nil || err != nil {
		t.Fatal("default must remain off")
	}
	body := cacheAuditBodyForTest(`[]`, `[]`)
	ident := Identity{Fingerprint: "private-routing-HMAC"}
	var off *mistralCacheAudit
	if n := testing.AllocsPerRun(100, func() { off.begin(nil, ident, body, body, time.Now()) }); n != 0 {
		t.Fatalf("disabled diagnostics allocated %v", n)
	}
	var offRequest *mistralAuditRequest
	if n := testing.AllocsPerRun(100, func() { offRequest.attempt(nil, nil, nil, config.Account{}, body, 0, nil) }); n != 0 {
		t.Fatalf("disabled attempt allocated %v", n)
	}
	a := cacheAuditForTest(t)
	for _, change := range []func(*requestLog){func(r *requestLog) { r.ClientKeyID = "client2" }, func(r *requestLog) { r.Provider = config.ProviderTokenHarbor }, func(r *requestLog) { r.Path = "/v1/fim/completions" }} {
		r := cacheAuditRecordForTest("excluded")
		change(r)
		if a.begin(r, ident, body, body, time.Now()) != nil || r.CacheAudit != nil || a.samples != 0 {
			t.Fatal("diagnostic scope escaped")
		}
	}
	first := a.begin(cacheAuditRecordForTest("one"), ident, body, body, time.Now())
	second := a.begin(cacheAuditRecordForTest("two"), ident, body, body, time.Now())
	if second.log.PreviousClient.RequestID != "one" || !second.log.PreviousClient.MayOverlap {
		t.Fatal("concurrent observation was silently treated as serial")
	}
	second.done.Store(true)
	third := a.begin(cacheAuditRecordForTest("three"), ident, body, body, time.Now())
	if third.log.PreviousPrepared.MayOverlap || third.log.SessionTag == ident.Fingerprint || third.log.SessionTag == "" || first.log.RunID == "" {
		t.Fatal("identity privacy/overlap missing")
	}
	isolated := cacheAuditRecordForTest("other-model")
	isolated.Model = "other"
	if a.begin(isolated, ident, body, body, time.Now()).log.PreviousClient != nil {
		t.Fatal("different models shared a history baseline")
	}
	for i := 0; i < 2*mistralAuditSessions; i++ {
		a.begin(cacheAuditRecordForTest(fmt.Sprint(i)), Identity{Fingerprint: fmt.Sprint(i)}, body, body, time.Now())
	}
	if len(a.history) > mistralAuditSessions {
		t.Fatal("history exceeded cap")
	}
	var wg sync.WaitGroup
	for i := 0; i < 2*mistralAuditRequests; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.begin(cacheAuditRecordForTest("concurrent"), ident, body, body, time.Now()) }()
	}
	wg.Wait()
	if a.samples != mistralAuditRequests {
		t.Fatalf("concurrent budget exceeded: %d", a.samples)
	}
	if a.begin(cacheAuditRecordForTest("expired"), ident, body, body, a.expires) != nil || len(a.history) != 0 {
		t.Fatal("TTL did not stop collection and release baselines")
	}
}
