package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perchstore"
)

// perchOpenEnv is a running server with an import key scoped to the Perch pool.
type perchOpenEnv struct {
	t   *testing.T
	srv *Server
	key string
}

func newPerchOpenEnv(t *testing.T) *perchOpenEnv {
	t.Helper()
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for !s.logs.Ready() {
		time.Sleep(5 * time.Millisecond)
	}
	ik := config.ImportKey{
		ID: "ik_perch", Name: "regbot", Group: "perch", Provider: config.ProviderPerch,
		KeyHash: config.HashKey("sk-odbimp-testperch"), KeyPrefix: "x", Enabled: true,
	}
	if _, err := s.cfg.Update(-1, func(c *config.Config) error {
		c.ImportKeys = []config.ImportKey{ik}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &perchOpenEnv{t: t, srv: s, key: "sk-odbimp-testperch"}
}

// post sends one body to the open Perch endpoint.
func (e *perchOpenEnv) post(body any, key string) (*httptest.ResponseRecorder, map[string]any) {
	e.t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/open/v1/perch/import", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if key == "" {
		key = e.key
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.RemoteAddr = "192.0.2.10:1234"
	rec := httptest.NewRecorder()
	e.srv.apiPerchOpenImport(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// exportDocAsMap is the account export in the documented shape.
func exportDocAsMap(uid string) map[string]any {
	return map[string]any{
		"schema_version": "1.0", "provider": "perchai", "auth_provider": "github",
		"account": map[string]any{
			"supabase_user_id": uid, "email": uid + "@gmail.com",
			"github_user_id": nil, "registered_at": "2026-11-01T03:00:00Z",
		},
		"session": map[string]any{
			"access_token": "access-" + uid, "refresh_token": "refresh-" + uid,
			"expires_at": time.Now().Add(time.Hour).Unix(),
		},
		"auth_context": map[string]any{"supabase_url": "https://sb.example.supabase.co", "workspace_id": nil},
	}
}

func (e *perchOpenEnv) count(uid string) int {
	n := 0
	for _, a := range e.srv.cfg.Get().Accounts {
		if a.Provider == config.ProviderPerch && a.PerchUserID == uid {
			n++
		}
	}
	return n
}

// A sign-up bot's export imports as one account.
func TestPerchOpenImportSingle(t *testing.T) {
	e := newPerchOpenEnv(t)
	rec, out := e.post(exportDocAsMap("bot-1"), "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %v", rec.Code, out)
	}
	if out["added"].(float64) != 1 {
		t.Fatalf("added = %v", out["added"])
	}
	if e.count("bot-1") != 1 {
		t.Fatal("the account was not created")
	}
	// the session really landed: the keeper knows it
	sec, ok := e.srv.perch.keeper.ByUserID("bot-1")
	if !ok || sec.RefreshToken != "refresh-bot-1" {
		t.Fatalf("session not stored: %+v", sec)
	}
	if sec.SupabaseURL != "https://sb.example.supabase.co" {
		t.Fatalf("supabase url = %q", sec.SupabaseURL)
	}
	a := e.srv.cfg.Get().Accounts[0]
	if a.Group != "perch" || !a.Enabled || a.PerchUserID != "bot-1" {
		t.Fatalf("account = %+v", a)
	}
	if a.RuntimeKey != config.PerchRuntimeKey("bot-1") {
		t.Fatalf("runtime key = %q", a.RuntimeKey)
	}
}

// Re-posting the same account is a no-op: a bot that retries must not build
// duplicates.
func TestPerchOpenImportIsIdempotent(t *testing.T) {
	e := newPerchOpenEnv(t)
	if rec, _ := e.post(exportDocAsMap("bot-1"), ""); rec.Code != 200 {
		t.Fatal("first import failed")
	}
	rec, out := e.post(exportDocAsMap("bot-1"), "")
	if rec.Code != 200 {
		t.Fatalf("re-import status %d", rec.Code)
	}
	if out["added"].(float64) != 0 || out["duplicate"].(float64) != 1 {
		t.Fatalf("counts = %v", out)
	}
	if e.count("bot-1") != 1 {
		t.Fatalf("a retry created %d accounts", e.count("bot-1"))
	}
	// A retry carrying a NEWER session updates rather than duplicating. The
	// store orders writes by fetchedAt and an import is stamped at arrival, so
	// the two imports must not land in the same instant to be orderable.
	time.Sleep(1100 * time.Millisecond)
	fresh := exportDocAsMap("bot-1")
	fresh["session"] = map[string]any{
		"access_token": "access-bot-1-v2", "refresh_token": "refresh-bot-1-v2",
		"expires_at": time.Now().Add(2 * time.Hour).Unix(),
	}
	_, out = e.post(fresh, "")
	if out["updated"].(float64) != 1 {
		t.Fatalf("a newer session should update: %v", out)
	}
	sec, _ := e.srv.perch.keeper.ByUserID("bot-1")
	if sec.RefreshToken != "refresh-bot-1-v2" {
		t.Fatalf("refresh token = %q", sec.RefreshToken)
	}
	if e.count("bot-1") != 1 {
		t.Fatal("update created a second account")
	}
}

// A batch reports every entry, and one bad entry costs only itself.
func TestPerchOpenImportBatchCountsEveryEntry(t *testing.T) {
	e := newPerchOpenEnv(t)
	docs := []any{
		exportDocAsMap("b-1"),
		exportDocAsMap("b-2"),
		map[string]any{"session": map[string]any{"access_token": "a", "refresh_token": "r"}}, // no user id
		exportDocAsMap("b-2"), // same account again in one batch
		"not an object",
	}
	rec, out := e.post(map[string]any{"accounts": docs}, "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %v", rec.Code, out)
	}
	if got := out["total"].(float64); got != 5 {
		t.Fatalf("total = %v", got)
	}
	if out["added"].(float64) != 2 {
		t.Fatalf("added = %v, want the two distinct accounts", out["added"])
	}
	// the in-batch duplicate and the two unreadable entries are reported
	if out["duplicate"].(float64) != 1 || out["failed"].(float64) != 2 {
		t.Fatalf("duplicate/failed = %v/%v", out["duplicate"], out["failed"])
	}
	if e.count("b-1") != 1 || e.count("b-2") != 1 {
		t.Fatal("accounts not created exactly once")
	}
	// every entry has a result with an index, so a bot can line them up
	results := out["results"].([]any)
	if len(results) != 5 {
		t.Fatalf("results = %d, want one per entry", len(results))
	}
	for i, raw := range results {
		r := raw.(map[string]any)
		if int(r["index"].(float64)) != i {
			t.Fatalf("result %d has index %v", i, r["index"])
		}
		if r["status"] == "error" && (r["error"] == nil || r["error"] == "") {
			t.Fatalf("entry %d failed without a reason", i)
		}
	}
}

// The flattened shape is accepted so a generator that builds the fields itself
// does not have to nest them.
func TestPerchOpenImportFlattened(t *testing.T) {
	e := newPerchOpenEnv(t)
	rec, out := e.post(map[string]any{
		"supabaseUserId": "flat-1",
		"email":          "flat@gmail.com",
		"accessToken":    "access-flat",
		"refreshToken":   "refresh-flat",
		"expiresAt":      time.Now().Add(time.Hour).Unix(),
	}, "")
	if rec.Code != 200 || out["added"].(float64) != 1 {
		t.Fatalf("flattened import: %d %v", rec.Code, out)
	}
	sec, ok := e.srv.perch.keeper.ByUserID("flat-1")
	if !ok || sec.RefreshToken != "refresh-flat" {
		t.Fatalf("session = %+v", sec)
	}
}

// A sessionJson string lets a bot forward the file it produced verbatim.
func TestPerchOpenImportSessionJSON(t *testing.T) {
	e := newPerchOpenEnv(t)
	doc, _ := json.Marshal(exportDocAsMap("sj-1"))
	rec, out := e.post(map[string]any{"sessionJson": string(doc)}, "")
	if rec.Code != 200 || out["added"].(float64) != 1 {
		t.Fatalf("sessionJson import: %d %v", rec.Code, out)
	}
	if _, ok := e.srv.perch.keeper.ByUserID("sj-1"); !ok {
		t.Fatal("session not stored")
	}
}

// Tags and a name override ride along with the import.
func TestPerchOpenImportTagsAndName(t *testing.T) {
	e := newPerchOpenEnv(t)
	rec, _ := e.post(map[string]any{
		"accounts": []any{exportDocAsMap("tag-1")},
		"tags":     []string{"batch-7", "regbot"},
		"name":     "机器人 7 号",
	}, "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	a := e.srv.cfg.Get().Accounts[0]
	if a.Name != "机器人 7 号" {
		t.Fatalf("name = %q", a.Name)
	}
	if len(a.PerchTags) != 2 {
		t.Fatalf("tags = %v", a.PerchTags)
	}
}

// The endpoint needs an import key, and a key scoped to another pool cannot
// create Perch accounts.
func TestPerchOpenImportAuth(t *testing.T) {
	e := newPerchOpenEnv(t)
	if rec, _ := e.post(exportDocAsMap("x"), "sk-odbimp-wrong"); rec.Code != 401 {
		t.Fatalf("a bad key gave %d", rec.Code)
	}
	// a key for another pool is refused
	other := config.ImportKey{ID: "ik_river", Name: "r", Group: "river", Provider: config.ProviderRiver,
		KeyHash: config.HashKey("sk-odbimp-river"), Enabled: true}
	if _, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
		c.ImportKeys = append(c.ImportKeys, other)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := e.post(exportDocAsMap("x"), "sk-odbimp-river"); rec.Code != 403 {
		t.Fatalf("a river key gave %d, want 403", rec.Code)
	}
	if e.count("x") != 0 {
		t.Fatal("a refused key created an account")
	}
}

// An empty body and an oversized batch are refused with a usable message.
func TestPerchOpenImportValidation(t *testing.T) {
	e := newPerchOpenEnv(t)
	if rec, out := e.post(map[string]any{}, ""); rec.Code != 400 {
		t.Fatalf("empty body gave %d %v", rec.Code, out)
	}
	many := make([]any, maxPerchImportBatch+1)
	for i := range many {
		many[i] = exportDocAsMap(fmt.Sprintf("n-%d", i))
	}
	if rec, _ := e.post(map[string]any{"accounts": many}, ""); rec.Code != 400 {
		t.Fatalf("an oversized batch gave %d", rec.Code)
	}
	// an entry with no token at all is refused with a reason
	rec, out := e.post(map[string]any{"account": map[string]any{"supabase_user_id": "no-token"}}, "")
	if rec.Code != 400 {
		t.Fatalf("a tokenless entry gave %d", rec.Code)
	}
	results := out["results"].([]any)
	if r := results[0].(map[string]any); r["status"] != "error" {
		t.Fatalf("result = %v", r)
	}
}

// A group override lands the accounts there instead of the pool's default.
func TestPerchOpenImportGroupOverride(t *testing.T) {
	e := newPerchOpenEnv(t)
	rec, _ := e.post(map[string]any{
		"accounts": []any{exportDocAsMap("g-1")},
		"group":    "team-x",
	}, "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if got := e.srv.cfg.Get().Accounts[0].Group; got != "team-x" {
		t.Fatalf("group = %q", got)
	}
}

// A bot that re-posts an OLD export must not undo a session the keeper has
// renewed since. This is the case that matters for a sign-up bot that keeps
// its own copies of what it produced.
func TestPerchOpenImportOldFileCannotUndoRenewal(t *testing.T) {
	e := newPerchOpenEnv(t)
	// an export issued an hour ago (its access token ends about now)
	old := exportDocAsMap("keep-1")
	old["session"] = map[string]any{
		"access_token": "access-old", "refresh_token": "refresh-old",
		"expires_at": time.Now().Add(-time.Minute).Unix(),
	}
	if rec, out := e.post(old, ""); rec.Code != 200 || out["added"].(float64) != 1 {
		t.Fatalf("first import: %d %v", rec.Code, out)
	}
	// the keeper renews it: the stored session becomes newer than the file
	stored, _ := e.srv.perch.keeper.ByUserID("keep-1")
	renewed := stored
	renewed.AccessToken, renewed.RefreshToken = "access-renewed", "refresh-renewed"
	renewed.ExpiresAt = time.Now().Add(90 * time.Minute).Unix()
	renewed.FetchedAt = time.Now().UTC()
	if _, err := e.srv.perch.keeper.Put(renewed, perchstore.PutOptions{BindAs: e.srv.cfg.Get().Accounts[0].ID, FetchedAt: renewed.FetchedAt}); err != nil {
		t.Fatal(err)
	}

	// the bot posts its old copy again
	_, out := e.post(old, "")
	if out["updated"].(float64) != 0 {
		t.Fatalf("an old file was applied: %v", out)
	}
	after, _ := e.srv.perch.keeper.ByUserID("keep-1")
	if after.RefreshToken != "refresh-renewed" || after.AccessToken != "access-renewed" {
		t.Fatalf("an old file undid the renewal: %+v", after)
	}
	if e.count("keep-1") != 1 {
		t.Fatal("the re-import created a second account")
	}
}
