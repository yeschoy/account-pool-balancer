package thregister

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/egressid"
)

type completionMailbox struct{ reject bool }

func (m completionMailbox) Messages(context.Context) ([]Message, error) {
	if m.reject {
		return nil, ErrMailboxRejected
	}
	return []Message{{ReceivedAt: time.Now(), Text: "https://tokenharbor.ai/verify-email?token=mock_completion"}}, nil
}

func TestCompletionOrdersAndResumes(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		for _, order := range []CompletionOrder{KeyThenVerify, VerifyThenKey} {
			t.Run(fmt.Sprintf("oauth=%t/%s", oauth, order), func(t *testing.T) {
				var events []string
				var saved Result
				flow, err := New(Config{Mailbox: completionMailbox{}, CompletionOrder: order,
					BrowserProfile: "edge-157-windows", Seed: "same-account",
					Checkpoint: func(res Result) error { saved = res; return nil }})
				if err != nil {
					t.Fatal(err)
				}
				defer flow.Close()
				flow.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("User-Agent") != egressid.ForProfile("same-account", "edge-157-windows").UserAgent {
						t.Error("all completion steps must retain the saved browser profile")
					}
					switch r.URL.Path {
					case "/api/keys":
						if r.Method == "POST" {
							if order == VerifyThenKey && !saved.VerifiedEmail {
								t.Error("verification was not checkpointed before key creation")
							}
							events = append(events, "key")
							return githubMockResponse(r, 201, `{"key":"thk_live_mock_completion_key_abcdefghijklmnopqrstuvwxyz0123456789"}`), nil
						}
						return githubMockResponse(r, 200, `[]`), nil
					case "/api/me/send-verification-email":
						if !saved.VerificationMailAttempted {
							t.Error("send marker must be durable BEFORE POST")
						}
						events = append(events, "send")
						return githubMockResponse(r, 200, `{"ok":true}`), nil
					case "/verify-email":
						if order == KeyThenVerify && saved.APIKey == "" {
							t.Error("one-time key not checkpointed before verification")
						}
						events = append(events, "verify")
						return githubMockResponse(r, 200, "verified"), nil
					default:
						t.Error("unexpected endpoint", r.URL.Path)
						return githubMockResponse(r, 500, ""), nil
					}
				})
				res, err := flow.Resume(context.Background(), Result{Email: "mock@local.test", OAuthComplete: oauth,
					Cookies: []NamedCookie{{Name: "sb-auth-auth-token", Value: "mock-session"}}})
				if err != nil || !res.VerifiedEmail || res.APIKey == "" || saved.CompletionOrder != order {
					t.Fatalf("completion failed: %v", err)
				}
				want := []string{"key", "verify"}
				if order == VerifyThenKey {
					want = []string{"verify", "key"}
				}
				if oauth {
					if order == VerifyThenKey {
						want = []string{"send", "verify", "key"}
					} else {
						want = []string{"key", "send", "verify"}
					}
				}
				if !reflect.DeepEqual(events, want) {
					t.Fatalf("got %v, want %v", events, want)
				}
				events = nil
				if _, err := flow.Resume(context.Background(), saved); err != nil || len(events) != 0 {
					t.Fatal("resume replayed key/send/verification", events, err)
				}
			})
		}
	}
}

func TestVerifyFirstFailureNeverCreatesKeyAndResumeKeepsOrder(t *testing.T) {
	var saved Result
	posts := 0
	f, _ := New(Config{CompletionOrder: VerifyThenKey, Mailbox: completionMailbox{reject: true},
		Checkpoint: func(res Result) error { saved = res; return nil }})
	defer f.Close()
	f.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
		posts++
		return githubMockResponse(r, 500, ""), nil
	})
	res, err := f.Resume(context.Background(), Result{Email: "mock@local.test", Cookies: []NamedCookie{{Name: "sb-auth-auth-token", Value: "saved"}}})
	if !errors.Is(err, ErrMailboxRejected) || posts != 0 || res.APIKey != "" || saved.CompletionOrder != VerifyThenKey || !HasSession(saved.Cookies) {
		t.Fatal("verify-first failed to stop before key endpoint", err, posts)
	}
	// An account's persisted order wins over a changed default on a later run.
	f.cfg.CompletionOrder = KeyThenVerify
	if _, err := f.Resume(context.Background(), saved); !errors.Is(err, ErrMailboxRejected) || posts != 0 {
		t.Fatal("resume changed order")
	}
}

func TestOAuthUncertainSendIsNotRepeatedOnVerifyFirstResume(t *testing.T) {
	var saved Result
	sends, keys := 0, 0
	f, _ := New(Config{CompletionOrder: VerifyThenKey, Mailbox: completionMailbox{reject: true},
		Checkpoint: func(res Result) error { saved = res; return nil }})
	defer f.Close()
	f.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/me/send-verification-email" {
			sends++
			return nil, errors.New("uncertain response")
		}
		keys++
		return githubMockResponse(r, 500, ""), nil
	})
	_, err := f.Resume(context.Background(), Result{Email: "mock@local.test", OAuthComplete: true, Cookies: []NamedCookie{{Name: "sb-auth-auth-token", Value: "saved"}}})
	if err == nil || sends != 1 || !saved.VerificationMailAttempted {
		t.Fatal("send attempt not preserved")
	}
	_, err = f.Resume(context.Background(), saved)
	if !errors.Is(err, ErrMailboxRejected) || sends != 1 || keys != 0 {
		t.Fatal("uncertain send was replayed or key created", err)
	}
}

func TestCompletionCheckpointFailurePreventsFollowingMutations(t *testing.T) {
	for _, failAt := range []string{"initial", "send", "verified", "key-reservation", "key"} {
		t.Run(failAt, func(t *testing.T) {
			calls := 0
			f, _ := New(Config{CompletionOrder: VerifyThenKey, Mailbox: completionMailbox{}, Checkpoint: func(res Result) error {
				if (failAt == "initial") || (failAt == "send" && res.VerificationMailAttempted) ||
					(failAt == "verified" && res.VerifiedEmail) ||
					(failAt == "key-reservation" && res.APIKeyCreationAttempted) || (failAt == "key" && res.APIKey != "") {
					return errors.New("disk blocked")
				}
				return nil
			}})
			defer f.Close()
			f.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path == "/api/me/send-verification-email" {
					return githubMockResponse(r, 200, `{"ok":true}`), nil
				}
				if r.Method == "POST" {
					return githubMockResponse(r, 201, `{"key":"thk_live_mock_checkpoint_key_abcdefghijklmnopqrstuvwxyz0123456789"}`), nil
				}
				return githubMockResponse(r, 200, `[]`), nil
			})
			_, err := f.Resume(context.Background(), Result{OAuthComplete: true, Cookies: []NamedCookie{{Name: "sb-auth-auth-token", Value: "saved"}}})
			want := map[string]int{"initial": 0, "send": 0, "verified": 2, "key-reservation": 3, "key": 4}[failAt]
			if err == nil || !strings.Contains(err.Error(), "disk blocked") || calls != want {
				t.Fatalf("calls=%d want=%d err=%v", calls, want, err)
			}
		})
	}
}

func TestUncertainKeyCreationIsNotRepeated(t *testing.T) {
	var saved Result
	calls, posts := 0, 0
	f, _ := New(Config{Checkpoint: func(res Result) error { saved = res; return nil }})
	defer f.Close()
	f.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method == "POST" {
			if !saved.APIKeyCreationAttempted {
				t.Error("key reservation not persisted before POST")
			}
			posts++
			return nil, errors.New("unknown key response")
		}
		return githubMockResponse(r, 200, `[]`), nil
	})
	_, err := f.Resume(context.Background(), Result{Email: "mock@local.test", Cookies: []NamedCookie{{Name: "sb-auth-auth-token", Value: "saved"}}})
	if err == nil || posts != 1 || saved.APIKey != "" || !saved.APIKeyCreationAttempted {
		t.Fatal("key uncertainty lost")
	}
	_, err = f.Resume(context.Background(), saved)
	if err == nil || posts != 1 || calls != 2 {
		t.Fatal("uncertain key creation automatically replayed", calls, posts)
	}
}

func TestVerificationErrorDoesNotExposeOneTimeLink(t *testing.T) {
	var saved Result
	f, _ := New(Config{CompletionOrder: VerifyThenKey, Mailbox: completionMailbox{}, Checkpoint: func(res Result) error { saved = res; return nil }})
	defer f.Close()
	f.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/verify-email" {
			t.Fatal("verify-first must not reach keys after verification fails")
		}
		return nil, errors.New("private_reflected_network_detail")
	})
	_, err := f.Resume(context.Background(), Result{Cookies: []NamedCookie{{Name: "sb-auth-auth-token", Value: "saved"}}})
	if err == nil || strings.Contains(err.Error(), "mock_completion") || strings.Contains(err.Error(), "private_reflected") || strings.Contains(err.Error(), "token=") || saved.APIKey != "" || saved.VerifiedEmail {
		t.Fatal("unsafe verification error or key created", err)
	}
}

func TestVerifyFirstTimeoutAndMissingMailboxDoNotFallBackToKey(t *testing.T) {
	for _, mailbox := range []Mailbox{nil, checkpointMailbox{}} {
		var saved Result
		f, _ := New(Config{CompletionOrder: VerifyThenKey, Mailbox: mailbox, MailTimeout: time.Millisecond, PollEvery: time.Millisecond, Checkpoint: func(res Result) error { saved = res; return nil }})
		f.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
			t.Error("timeout must not fall back to key creation")
			return githubMockResponse(r, 500, ""), nil
		})
		_, err := f.Resume(context.Background(), Result{Cookies: []NamedCookie{{Name: "sb-auth-auth-token", Value: "saved"}}})
		f.Close()
		if err == nil || saved.APIKey != "" || saved.CompletionOrder != VerifyThenKey || saved.APIKeyCreationAttempted {
			t.Fatal("verify-first changed order on timeout", err)
		}
	}
}

func TestCompletionOrderDistributionIsIndependentAndStable(t *testing.T) {
	counts := map[CompletionOrder]int{}
	for i := range 2000 {
		seed := fmt.Sprint(i)
		o := CompletionOrderFor(seed)
		counts[o]++
		if o != CompletionOrderFor(seed) {
			t.Fatal("unstable order")
		}
	}
	if counts[KeyThenVerify] < 800 || counts[VerifyThenKey] < 800 {
		t.Fatal("biased completion distribution", counts)
	}
}
