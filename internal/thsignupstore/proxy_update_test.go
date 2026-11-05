package thsignupstore

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"account-pool-balancer/internal/thregister"
)

func TestSetProxyForIdleRowsPreservesCredentialsAndPersists(t *testing.T) {
	for _, status := range []Status{Saved, Registered, Unverified, Imported, Failed, Uncertain} {
		t.Run(string(status), func(t *testing.T) {
			s, path := openTemp(t)
			rows, err := s.AddSaved("proxy", []Row{{Email: "proxy@mock.test", Password: "private-password", ProxyURL: "http://old:8080", APIKey: "thk_live_private", MailboxURL: "https://mock.test/messages/private"}})
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.Update(rows[0].ID, func(r *Row) error {
				r.Status, r.Account, r.Verified = status, "account", true
				r.Cookies = []Cookie{{Name: "session", Value: "private-cookie"}}
				r.Checkout = &Checkout{URL: "https://checkout.stripe.com/c/pay/mock_only"}
				r.Subscription = thregister.Subscription{Status: "active", Tier: "plus", CheckedAt: time.Now()}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			proxy := "socks5h://user:private-proxy@[::1]:1080"
			after, err := s.SetProxy(before.ID, "  "+proxy+"  ")
			if err != nil {
				t.Fatal(err)
			}
			if after.ProxyURL != proxy || !after.ProxyBound || after.Subscription.Status != "" {
				t.Fatal("proxy not bound or old subscription reused")
			}
			want := before
			want.ProxyURL, want.ProxyBound, want.Subscription, want.UpdatedAt = after.ProxyURL, after.ProxyBound, after.Subscription, after.UpdatedAt
			if !reflect.DeepEqual(after, want) {
				t.Fatal("proxy change modified credentials or lifecycle")
			}
			reopened, err := Open(path, secret)
			if err != nil {
				t.Fatal(err)
			}
			persisted, _ := reopened.Get(before.ID)
			if !persisted.CreatedAt.Equal(after.CreatedAt) || !persisted.UpdatedAt.Equal(after.UpdatedAt) {
				t.Fatal("persisted timestamps changed")
			}
			// JSON drops time.Time's monotonic clock readings.
			after.CreatedAt, after.UpdatedAt = persisted.CreatedAt, persisted.UpdatedAt
			if !reflect.DeepEqual(persisted, after) {
				t.Fatal("proxy change not persisted")
			}
			direct, err := reopened.SetProxy(before.ID, "")
			if err != nil || direct.Egress("http://default:8080") != "" {
				t.Fatal("explicit direct route inherited default", err)
			}
		})
	}
}

func TestSetProxyRejectsBusyAndInvalidWithoutMutation(t *testing.T) {
	for _, status := range []Status{Queued, Running, Saved} {
		t.Run(string(status), func(t *testing.T) {
			s, _ := openTemp(t)
			rows, _ := s.AddSaved("proxy", []Row{{Email: "busy@mock.test", ProxyURL: "http://old:8080"}})
			before, _ := s.Update(rows[0].ID, func(r *Row) error { r.Status = status; return nil })
			inputs := []string{"socks4://user:private@host:123", "http://user:private@host:8080/path", "not-a-proxy"}
			if status != Saved {
				inputs = append(inputs, "http://valid:8080", "")
			}
			for _, proxy := range inputs {
				if _, err := s.SetProxy(before.ID, proxy); err == nil {
					t.Fatal("invalid or busy proxy edit accepted")
				}
				after, _ := s.Get(before.ID)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejected edit changed row")
				}
			}
		})
	}
	s, _ := openTemp(t)
	if _, err := s.SetProxy("missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing row not reported", err)
	}
}
