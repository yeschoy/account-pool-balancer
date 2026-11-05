package thregister

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const checkoutTestURL = "https://checkout.stripe.com/c/pay/cs_test_secret#session"
const freeAgentQuote = `{"quote":{"kind":"tier","fromTier":"free","oneTime":true,"period":"monthly","planName":"Agent Pass","payNowUsd":0.99}}`

var agentCheckout = CheckoutRequest{Kind: "pass", Product: "starter_pass_monthly"}

func TestCheckoutPreviewsFreeAccountThenCreatesOnlyHostedPage(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/billing/subscribe" || r.Header.Get("Origin") == "" || r.Header.Get("Cookie") != "session=saved" {
			t.Error("missing method, endpoint, origin or session")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["autoRenew"] != false || body["period"] != "monthly" || body["plan"] != "starter_pass_monthly" || len(body) != 4 {
			t.Error("unsafe purchase request", body)
		}
		if calls == 1 {
			if body["preview"] != true {
				t.Error("must preview before checkout")
			}
			w.Write([]byte(freeAgentQuote))
			return
		}
		if _, ok := body["preview"]; ok {
			t.Error("hosted checkout request must not include preview")
		}
		if body["expectedPayNowCents"] != float64(99) {
			t.Error("quote must be pinned")
		}
		json.NewEncoder(w).Encode(map[string]string{"checkout_url": checkoutTestURL})
	}))
	defer srv.Close()
	f, _ := New(Config{BaseURL: srv.URL})
	defer f.Close()
	f.SeedCookies([]NamedCookie{{Name: "session", Value: "saved"}})
	link, err := f.CreateCheckout(context.Background(), agentCheckout)
	if err != nil || link != checkoutTestURL || calls != 2 {
		t.Fatalf("checkout: link=%t calls=%d err=%v", link != "", calls, err)
	}
}

func TestCheckoutStopsBeforeUpgradeOrUnknownQuote(t *testing.T) {
	for _, quote := range []string{
		`{}`, `{"upgraded":true}`, `{"checkout_url":"` + checkoutTestURL + `"}`,
		strings.Replace(freeAgentQuote, `"free"`, `"plus"`, 1),
		strings.Replace(freeAgentQuote, `"oneTime":true`, `"oneTime":false`, 1),
		strings.Replace(freeAgentQuote, `"monthly"`, `"yearly"`, 1),
		strings.Replace(freeAgentQuote, `0.99`, `999`, 1),
		strings.Replace(freeAgentQuote, `0.99`, `0.001`, 1),
		strings.Replace(freeAgentQuote, `0.99`, `0`, 1),
	} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(quote)) }))
		f, _ := New(Config{BaseURL: srv.URL})
		_, err := f.CreateCheckout(context.Background(), agentCheckout)
		f.Close()
		srv.Close()
		if err == nil || calls != 1 {
			t.Fatal("unsafe preview must stop before checkout")
		}
	}
}

func TestCheckoutRefusesRedirectErrorsAndUnsafeLinks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"changed quote", 200, freeAgentQuote},
		{"upgrade", 200, `{"upgraded":true}`},
		{"redirect", 307, ""}, {"unauthorized", 401, `{"error":"secret_cookie"}`},
		{"region", 409, `{"region_warning_required":true,"error":"secret_cookie"}`},
		{"limit", 429, "secret_cookie"}, {"server", 500, "secret_cookie"},
		{"invalid json", 200, "secret_cookie"}, {"missing link", 200, `{}`},
		{"javascript", 200, `{"checkout_url":"javascript:alert(1)"}`},
		{"foreign host", 200, `{"checkout_url":"https://checkout.stripe.com.evil.test/c/pay/secret_cookie"}`},
		{"userinfo", 200, `{"checkout_url":"https://secret_cookie@checkout.stripe.com/c/pay/id"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					w.Write([]byte(freeAgentQuote))
					return
				}
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			f, _ := New(Config{BaseURL: srv.URL})
			defer f.Close()
			if _, err := f.CreateCheckout(context.Background(), agentCheckout); err == nil || strings.Contains(err.Error(), "secret_cookie") {
				t.Fatalf("unsafe response must fail without echoing: %v", err)
			}
			if calls != 2 {
				t.Fatal("must not follow redirects, confirm changed quotes or retry")
			}
		})
	}
}

func TestCheckoutGuardRunsOnlyAfterValidPreviewAndCanStopCreation(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid preview", true: "persistence failure"}[valid], func(t *testing.T) {
			calls, guarded := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if valid {
					w.Write([]byte(freeAgentQuote))
				} else {
					w.WriteHeader(503)
				}
			}))
			defer srv.Close()
			f, _ := New(Config{BaseURL: srv.URL})
			defer f.Close()
			persistErr := errors.New("cannot persist reservation")
			_, err := f.CreateCheckoutGuarded(context.Background(), agentCheckout, func() error {
				guarded++
				return persistErr
			})
			if err == nil || calls != 1 || (guarded == 1) != valid || (valid && !errors.Is(err, persistErr)) {
				t.Fatal("invalid preview or failed persistence reached order creation")
			}
		})
	}
}

func TestCheckoutRejectsOtherProductsBeforeHTTP(t *testing.T) {
	f, _ := New(Config{BaseURL: "http://127.0.0.1:1"})
	defer f.Close()
	for _, in := range []CheckoutRequest{{}, {Kind: "topup", Product: "starter"}, {Kind: "pass", Product: "pro_pass_monthly"}, {Kind: "pass", Product: "frontier_pass_monthly"}} {
		if _, err := f.CreateCheckout(context.Background(), in); err == nil || strings.Contains(err.Error(), "网络") {
			t.Fatal("invalid product must fail before HTTP")
		}
	}
}
