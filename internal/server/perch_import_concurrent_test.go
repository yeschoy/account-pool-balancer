package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestPerchImportDuringConfigCallback(t *testing.T) {
	e := newPerchOpenEnv(t)
	raw, _ := json.Marshal(exportDocAsMap("pending-import"))
	entered := make(chan struct{})
	release := make(chan struct{})
	configDone := make(chan error, 1)
	go func() {
		_, err := e.srv.cfg.Update(-1, func(*config.Config) error {
			close(entered)
			<-release
			return nil
		})
		configDone <- err
	}()
	<-entered
	done := make(chan registrationRow, 1)
	go func() {
		row := registrationRow{Login: "user", Status: "activated", CanImport: true, doc: raw}
		e.srv.importRegistered(&row, "perch")
		done <- row
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := e.srv.perch.keeper.ByUserID("pending-import"); ok {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("import did not write its session")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-configDone; err != nil {
		t.Fatal(err)
	}
	select {
	case row := <-done:
		if row.Status != "imported" {
			t.Fatalf("registration failed: %+v", row)
		}
		if sec, ok := e.srv.perch.keeper.Get(row.AccountID); !ok || sec.RefreshToken != "refresh-pending-import" {
			t.Fatalf("config callback removed pending session: %+v", sec)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("config callback and import deadlocked")
	}
}

func TestPerchImportConcurrentEntrypoints(t *testing.T) {
	e := newPerchOpenEnv(t)
	doc := exportDocAsMap("concurrent-import")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	body, contentType := upload(t, map[string][]byte{"session.json": raw})
	row := registrationRow{Login: "user", Status: "activated", CanImport: true, doc: raw}
	type result struct {
		kind string
		code int
		data map[string]any
	}
	results := make(chan result, 3)
	start := make(chan struct{})
	go func() {
		<-start
		req := httptest.NewRequest(http.MethodPost, "/api/perch/import", body)
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		e.srv.apiPerchImport(rec, req)
		var data map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &data)
		results <- result{"manual", rec.Code, data}
	}()
	go func() {
		<-start
		req := httptest.NewRequest(http.MethodPost, "/open/v1/perch/import", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+e.key)
		req.RemoteAddr = "192.0.2.10:1234"
		rec := httptest.NewRecorder()
		e.srv.apiPerchOpenImport(rec, req)
		var data map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &data)
		results <- result{"open", rec.Code, data}
	}()
	go func() {
		<-start
		e.srv.importRegistered(&row, "perch")
		results <- result{kind: "registration"}
	}()
	close(start)
	collected := make([]result, 0, 3)
	for len(collected) < 3 {
		select {
		case r := <-results:
			collected = append(collected, r)
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent imports did not finish")
		}
	}
	if e.count("concurrent-import") != 1 {
		t.Fatal("expected exactly one account")
	}
	var accountID string
	for _, a := range e.srv.cfg.Get().Accounts {
		if a.PerchUserID == "concurrent-import" {
			accountID = a.ID
		}
	}
	if row.Status != "imported" || row.AccountID != accountID {
		t.Fatalf("registration result: %+v, account %q", row, accountID)
	}
	if sec, ok := e.srv.perch.keeper.Get(accountID); !ok || sec.RefreshToken != "refresh-concurrent-import" {
		t.Fatalf("missing or incorrect bound session: %+v", sec)
	}
	created := 0
	for _, r := range collected {
		switch r.kind {
		case "manual":
			if r.code != http.StatusOK {
				t.Fatalf("manual: %d %v", r.code, r.data)
			}
			sum := r.data["summary"].(map[string]any)
			created += int(sum["created"].(float64))
			out := sum["outcomes"].([]any)[0].(map[string]any)
			if out["accountId"] != accountID {
				t.Fatalf("manual outcome: %v", out)
			}
		case "open":
			if r.code != http.StatusOK {
				t.Fatalf("open: %d %v", r.code, r.data)
			}
			created += int(r.data["added"].(float64))
			out := r.data["results"].([]any)[0].(map[string]any)
			if out["accountId"] != accountID {
				t.Fatalf("open outcome: %v", out)
			}
		}
	}
	if created > 1 {
		t.Fatalf("multiple entries reported created: %d", created)
	}
}
