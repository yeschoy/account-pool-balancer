package thmailbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/thregister"
)

func TestCodeMailboxResponseFormats(t *testing.T) {
	for _, tc := range []struct {
		name, body           string
		wantMessages         int
		permanent, transient bool
	}{
		{"pending", `{"success":false,"code":"no_code","message":"private-key","retryable":true}`, 0, false, false},
		{"no-mail", `{"success":false,"code":"no_mail","retryable":true}`, 0, false, false},
		{"temporary", `{"success":false,"code":"busy","retryable":true}`, 0, false, true},
		{"denied", `{"success":false,"code":"invalid_key","message":"private-key","retryable":false}`, 0, true, false},
		{"verification-link", `{"success":true,"code":"https://tokenharbor.ai/verify-email?token=mock-link","received_at":"2026-11-07T01:02:03Z","email":"test@icloud.com","subject":"Verify email"}`, 1, false, false},
		{"html-link", `{"success":true,"code":"","html":"<a href=\"https://tokenharbor.ai/verify-email?token=mock-link\">verify</a>","received_at":"2026-11-07T01:02:03+00:00"}`, 1, false, false},
		{"numeric-not-link", `{"success":true,"code":"123456","received_at":"2026-11-07T01:02:03Z"}`, 0, true, false},
		{"foreign-link", `{"success":true,"code":"https://evil.test/verify-email?token=mock-link","received_at":"2026-11-07T01:02:03Z"}`, 0, true, false},
		{"no-date", `{"success":true,"code":"https://tokenharbor.ai/verify-email?token=mock-link"}`, 0, true, false},
		{"different-mailbox", `{"success":true,"email":"other@icloud.com","code":"https://tokenharbor.ai/verify-email?token=mock-link","received_at":"2026-11-07T01:02:03Z"}`, 0, true, false},
		{"html-response", `<p>private-key</p>`, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages, err := parseCodeMessages([]byte(tc.body), "test@icloud.com")
			if len(messages) != tc.wantMessages || errors.Is(err, thregister.ErrMailboxRejected) != tc.permanent || (err != nil) != (tc.permanent || tc.transient) {
				t.Fatalf("wrong parser state: messages=%d err=%v", len(messages), err)
			}
			if err != nil && strings.Contains(err.Error(), "private-key") {
				t.Fatal("reflected mailbox secret")
			}
			if len(messages) > 0 && (!messages[0].ReceivedAt.Equal(time.Date(2026, 11, 7, 1, 2, 3, 0, time.UTC)) || messages[0].Text != "https://tokenharbor.ai/verify-email?token=mock-link") {
				t.Fatal("mail time/link was lost or invented")
			}
		})
	}
}

func TestCodeMailboxURLValidation(t *testing.T) {
	valid := "https://mail.test/api/v1/mailboxes/test%40icloud.com/code?key=private-key"
	if err := Validate(valid); err != nil || CodeMailboxEmail(valid) != "test@icloud.com" {
		t.Fatalf("valid code endpoint rejected: %v", err)
	}
	for _, raw := range []string{
		"http://mail.test/api/v1/mailboxes/test@icloud.com/code?key=private-key",
		"https://user:private-key@mail.test/api/v1/mailboxes/test@icloud.com/code?key=private-key",
		"https://mail.test/api/v1/mailboxes/invalid/code?key=private-key",
		"https://mail.test/api/v1/mailboxes/test@icloud.com/code",
		"https://mail.test/api/v1/mailboxes/test@icloud.com/code?key=a&key=b",
		valid + "#private-key",
	} {
		if err := Validate(raw); err == nil || strings.Contains(err.Error(), "private-key") {
			t.Fatal("invalid or secret-bearing validation result")
		}
	}
}

func TestCodeMailboxRequestsAndRedirectIsolation(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprint(redirect), func(t *testing.T) {
			evilCalls := 0
			evil := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				evilCalls++
				t.Error("forwarded mailbox key through a redirect")
			}))
			defer evil.Close()
			calls := 0
			site := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/api/v1/mailboxes/test@icloud.com/code" || r.URL.Query().Get("key") != "private-key" || r.Header.Get("Accept") != "application/json" {
					t.Error("wrong code request")
				}
				if redirect {
					w.Header().Set("Location", evil.URL+"/?key=private-key")
					w.WriteHeader(302)
					return
				}
				fmt.Fprint(w, `{"success":false,"code":"no_code","retryable":true}`)
			}))
			defer site.Close()
			client := New(site.URL+"/api/v1/mailboxes/test@icloud.com/code?key=private-key", site.Client())
			messages, err := client.Messages(context.Background())
			if calls != 1 || evilCalls != 0 || len(messages) != 0 || (err != nil) != redirect {
				t.Fatalf("wrong request/redirect state: %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), "private-key") || strings.Contains(err.Error(), site.URL)) {
				t.Fatal("mailbox URL leaked")
			}
		})
	}
}
