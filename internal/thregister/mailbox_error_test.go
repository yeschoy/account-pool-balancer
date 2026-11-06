package thregister

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type rejectedVerificationMailbox struct{ calls int }

func (m *rejectedVerificationMailbox) Messages(context.Context) ([]Message, error) {
	m.calls++
	return nil, fmt.Errorf("%w: unsupported verification response", ErrMailboxRejected)
}

func TestMailboxPermanentFailurePreservesKeyAndDoesNotPollAgain(t *testing.T) {
	mailbox := &rejectedVerificationMailbox{}
	var saved Result
	keyCalls := 0
	flow, _ := New(Config{Mailbox: mailbox, Checkpoint: func(res Result) error { saved = res; return nil }})
	defer flow.Close()
	flow.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/keys" {
			t.Error("unexpected verification/consent request")
			return githubMockResponse(r, 500, ""), nil
		}
		if r.Method == "POST" {
			keyCalls++
			return githubMockResponse(r, 201, `{"key":"thk_live_abcdefghijklmnopqrstuvwxyz0123456789"}`), nil
		}
		return githubMockResponse(r, 200, `[]`), nil
	})
	res, err := flow.Resume(context.Background(), Result{Email: "expected@mock.test", Cookies: []NamedCookie{{Name: "sb-auth-auth-token", Value: "mock-session"}}})
	if !errors.Is(err, ErrMailboxRejected) || mailbox.calls != 1 || keyCalls != 1 || saved.APIKey == "" || res.APIKey != saved.APIKey || !HasSession(saved.Cookies) || res.VerifiedEmail || res.MailTimedOut {
		t.Fatalf("permanent mailbox failure lost credentials or faked timeout/success: %v", err)
	}
	if !strings.Contains(err.Error(), "unsupported verification response") {
		t.Fatal("mailbox error was swallowed")
	}
}
