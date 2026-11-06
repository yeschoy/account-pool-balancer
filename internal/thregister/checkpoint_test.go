package thregister

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSignupCheckpointsSessionBeforeKeyFailure(t *testing.T) {
	var snapshots []Result
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if r.Method == "GET" {
				w.Write([]byte(`<input name="$ACTION_1:0" value="1234567890123456789012345678901234567890"><input name="$ACTION_KEY" value="key">`))
			} else {
				http.SetCookie(w, &http.Cookie{Name: "sb-test-auth-token", Value: "saved-session", Path: "/"})
				w.Write([]byte("NEXT_REDIRECT"))
			}
		case "/api/auth/signup-precheck":
			w.Write([]byte(`{"needCaptcha":false}`))
		case "/api/keys":
			if r.Method == "POST" {
				w.WriteHeader(500)
			} else {
				w.Write([]byte(`{"keys":[]}`))
			}
		default:
			t.Error("unexpected endpoint, especially privacy consent", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	f, _ := New(Config{BaseURL: srv.URL, Checkpoint: func(r Result) error { snapshots = append(snapshots, r); return nil }})
	defer f.Close()
	res, err := f.Run(context.Background(), Credentials{Email: "test@example.com", Password: "pw"})
	if err == nil || len(snapshots) != 2 || len(snapshots[0].Cookies) == 0 || len(res.Cookies) == 0 ||
		snapshots[0].APIKeyCreationAttempted || !snapshots[1].APIKeyCreationAttempted || snapshots[1].APIKey != "" {
		t.Fatalf("session must survive key failure: snapshots=%d err=%v", len(snapshots), err)
	}
}

type checkpointMailbox struct{ messages []Message }

func (m checkpointMailbox) Messages(context.Context) ([]Message, error) { return m.messages, nil }

func TestResumeMailboxCutoffUsesOriginalRegistrationTime(t *testing.T) {
	link := "https://tokenharbor.ai/verify-email?token=mock_only"
	f, _ := New(Config{VerificationSince: time.Now().Add(-2 * time.Hour),
		Mailbox: checkpointMailbox{[]Message{{ReceivedAt: time.Now().Add(-time.Hour), Text: link}}}})
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if got, err := f.waitVerificationLink(ctx, "test@example.com"); err != nil || got != link {
		t.Fatal("resuming must not ignore mail received earlier in the same registration")
	}
}

func TestVerificationHTTPFailureIsNotSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	f, _ := New(Config{BaseURL: srv.URL})
	defer f.Close()
	if err := f.verifyEmail(context.Background(), srv.URL+"/verify"); err == nil {
		t.Fatal("HTTP 500 must not verify an account")
	}
}

func TestKeyIsCheckpointedBeforeAnyLaterWorkAndResumeDoesNotSignup(t *testing.T) {
	creates := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/keys" {
			t.Error("resume must not signup or consent", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		if r.Method == "POST" {
			creates++
			w.Write([]byte(`{"plaintext":"` + realKey + `"}`))
		} else {
			w.Write([]byte(`{"keys":[]}`))
		}
	}))
	defer srv.Close()
	var saved Result
	f, _ := New(Config{BaseURL: srv.URL, Checkpoint: func(r Result) error {
		saved = r
		if r.APIKey != "" {
			return errors.New("disk unavailable")
		}
		return nil
	}})
	defer f.Close()
	res, err := f.Resume(context.Background(), Result{Email: "test@example.com", Cookies: []NamedCookie{{Name: "sb-test-auth-token", Value: "saved"}}})
	if err == nil || saved.APIKey != realKey || res.APIKey != realKey || creates != 1 {
		t.Fatal("key must reach persistence callback and partial result immediately")
	}
	f2, _ := New(Config{BaseURL: srv.URL})
	defer f2.Close()
	if _, err = f2.Resume(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	if creates != 1 {
		t.Fatal("resume must retain existing key without creating another")
	}
}
