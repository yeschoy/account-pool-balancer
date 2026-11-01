package perchimport

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

// sampleJSON is the export format exactly as the CLI writes it.
const sampleJSON = `{
  "schema_version": "1.0",
  "provider": "perchai",
  "auth_provider": "github",
  "account": {
    "supabase_user_id": "11111111-2222-3333-4444-555555555555",
    "email": "example@gmail.com",
    "github_user_id": null,
    "registered_at": "2026-11-01T03:00:00Z"
  },
  "session": {
    "access_token": "ACCESS_TOKEN",
    "refresh_token": "REFRESH_TOKEN",
    "expires_at": 1800000000
  },
  "auth_context": {
    "supabase_url": "https://verified.supabase.co",
    "workspace_id": null
  }
}`

func TestParseExportFormat(t *testing.T) {
	p, err := ParseJSON([]byte(sampleJSON))
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("userId = %q", p.UserID)
	}
	if p.Email != "example@gmail.com" || p.AuthProvider != "github" {
		t.Fatalf("email/provider = %q/%q", p.Email, p.AuthProvider)
	}
	if p.AccessToken != "ACCESS_TOKEN" || p.RefreshToken != "REFRESH_TOKEN" {
		t.Fatal("tokens not read")
	}
	if p.ExpiresAt != 1800000000 {
		t.Fatalf("expires = %d", p.ExpiresAt)
	}
	if p.SupabaseURL != "https://verified.supabase.co" {
		t.Fatalf("supabase url = %q", p.SupabaseURL)
	}
	s := p.Secrets()
	if s.UserID != p.UserID || s.RefreshToken != "REFRESH_TOKEN" {
		t.Fatal("secrets do not carry the parsed values")
	}
}

func TestParseRejectsWithoutIdentity(t *testing.T) {
	_, err := ParseJSON([]byte(`{"session":{"access_token":"a","refresh_token":"r"}}`))
	if err == nil || !strings.Contains(err.Error(), "supabase_user_id") {
		t.Fatalf("want a user-id error, got %v", err)
	}
	_, err = ParseJSON([]byte(`{"account":{"supabase_user_id":"u"},"session":{}}`))
	if err == nil || !strings.Contains(err.Error(), "both empty") {
		t.Fatalf("want a token error, got %v", err)
	}
}

// A file with no expires_at still schedules correctly: the token's own exp
// claim is read.
func TestParseDerivesExpiryFromToken(t *testing.T) {
	// header.payload signature; payload is {"exp":1800000000,"email":"a@b.c"}
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjE4MDAwMDAwMDAsImVtYWlsIjoiYWJAYy5kIn0.sig"
	p, err := ParseJSON([]byte(`{"account":{"supabase_user_id":"u1"},"session":{"access_token":"` + jwt + `","refresh_token":"r"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.ExpiresAt != 1800000000 {
		t.Fatalf("expires = %d, want 1800000000", p.ExpiresAt)
	}
	if p.Email != "ab@c.d" {
		t.Fatalf("email = %q, want it read from the token", p.Email)
	}
}

func TestReadBatchAndZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	add("accounts/a.json", sampleJSON)
	add("accounts/bad.json", `{"account":{}}`)
	add("__MACOSX/._a.json", "junk")
	add("accounts/notes.txt", "not an account")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	files := map[string][]byte{
		"batch.zip": buf.Bytes(),
		"single.json": []byte(strings.Replace(sampleJSON, "11111111-2222-3333-4444-555555555555",
			"99999999-2222-3333-4444-555555555555", 1)),
		"broken.json": []byte("{nope"),
	}
	res := ReadFiles(files)
	if res.Parsed != 2 {
		t.Fatalf("parsed %d, want 2 (one from the zip, one loose)", res.Parsed)
	}
	if len(res.Failed) != 2 {
		t.Fatalf("failures = %v, want the bad member and the broken file", res.Failed)
	}
	var labels []string
	for _, f := range res.Failed {
		labels = append(labels, f.File)
	}
	joined := strings.Join(labels, ",")
	if !strings.Contains(joined, "batch.zip:accounts/bad.json") || !strings.Contains(joined, "broken.json") {
		t.Fatalf("failures should name their file: %v", res.Failed)
	}
	// the bad member must not have cost the good one in the same archive
	if res.ParsedDocs[0].UserID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("the good member was not read: %q", res.ParsedDocs[0].UserID)
	}
}

func TestPreviewFlagsDuplicates(t *testing.T) {
	files := map[string][]byte{
		"1.json": []byte(sampleJSON),
		"2.json": []byte(sampleJSON), // same user id
		"3.json": []byte(strings.Replace(sampleJSON, "11111111-2222-3333-4444-555555555555",
			"already-stored", 1)),
	}
	res := ReadFiles(files)
	items := Preview(res, func(uid string) bool { return uid == "already-stored" })
	if len(items) != 3 {
		t.Fatalf("got %d items", len(items))
	}
	byFile := map[string]Item{}
	for _, it := range items {
		byFile[it.File] = it
	}
	if byFile["1.json"].DuplicateOf != "" {
		t.Fatalf("1.json should be new: %+v", byFile["1.json"])
	}
	if byFile["2.json"].DuplicateOf != "1.json" {
		t.Fatalf("2.json should duplicate 1.json: %+v", byFile["2.json"])
	}
	if byFile["3.json"].DuplicateOf != "stored" {
		t.Fatalf("3.json should duplicate the pool: %+v", byFile["3.json"])
	}
	if !byFile["1.json"].HasRefresh {
		t.Fatal("a file with a refresh token must say so")
	}
	if byFile["1.json"].Expired {
		t.Fatal("expires_at 1800000000 (Jan 2027) is still in the future")
	}
	if byFile["1.json"].ExpiresAt.IsZero() || byFile["1.json"].ExpiresAt.Location() == time.UTC {
		// only assert it is the stated instant, the zone is the reader's
		if got := byFile["1.json"].ExpiresAt.Unix(); got != 1800000000 {
			t.Fatalf("expiry = %d", got)
		}
	}
}

func TestHundredFilesAllParsed(t *testing.T) {
	files := map[string][]byte{}
	for i := 0; i < 100; i++ {
		body := strings.Replace(sampleJSON, "11111111-2222-3333-4444-555555555555", fmt.Sprintf("user-%03d", i), 1)
		files[fmt.Sprintf("acct-%03d.json", i)] = []byte(body)
	}
	res := ReadFiles(files)
	if res.Parsed != 100 || len(res.Failed) != 0 {
		t.Fatalf("parsed %d, failed %d", res.Parsed, len(res.Failed))
	}
	seen := map[string]bool{}
	for _, p := range res.ParsedDocs {
		if seen[p.UserID] {
			t.Fatalf("duplicate user id %s after a distinct-file import", p.UserID)
		}
		seen[p.UserID] = true
	}
}
