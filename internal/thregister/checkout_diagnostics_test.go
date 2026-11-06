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

func TestCheckoutDiagnosticsPreserveStageAndAllowlistedCodeOnly(t *testing.T) {
	for _, stage := range []string{"preview", "create"} {
		for _, tc := range []struct {
			name, body, code string
			status           int
		}{
			{"provider", `{"error":"stripe_session_failed","message":"cookie-private bearer-private"}`, "stripe_session_failed", 502},
			{"nested-region", `{"error":{"code":"region_confirmation_required","message":"cookie-private"}}`, "region_confirmation_required", 403},
			{"unknown", `{"error":"cookie-private bearer-private"}`, "", 500},
			{"business-error-on-200", `{"error":"payments_disabled"}`, "payments_disabled", 200},
		} {
			t.Run(stage+"/"+tc.name, func(t *testing.T) {
				calls := 0
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if stage == "create" && calls == 1 {
						w.Write([]byte(freeAgentQuote))
						return
					}
					w.WriteHeader(tc.status)
					w.Write([]byte(tc.body))
				}))
				defer up.Close()
				f, _ := New(Config{BaseURL: up.URL})
				defer f.Close()
				_, err := f.CreateCheckout(context.Background(), agentCheckout)
				var diagnostic *CheckoutFailure
				if !errors.As(err, &diagnostic) || diagnostic.Stage != stage || diagnostic.Code != tc.code || diagnostic.HTTPStatus != tc.status || diagnostic.Uncertain != (stage == "create") {
					t.Fatalf("unsafe or missing diagnostic: %+v %v", diagnostic, err)
				}
				raw, _ := json.Marshal(diagnostic)
				if strings.Contains(string(raw)+err.Error(), "private") {
					t.Fatal("diagnostic echoed raw error/body")
				}
				want := 1
				if stage == "create" {
					want = 2
				}
				if calls != want {
					t.Fatal("error triggered replay", calls)
				}
			})
		}
	}
}
func TestCheckoutFailureOfNilAndEmptyBusinessError(t *testing.T) {
	var typedNil *CheckoutFailure
	if CheckoutFailureOf(nil) != nil || CheckoutFailureOf(typedNil) != nil {
		t.Fatal("nil error produced a diagnostic")
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.TrimSuffix(freeAgentQuote, "}") + `,"error":""}`))
	}))
	defer up.Close()
	flow, _ := New(Config{BaseURL: up.URL})
	defer flow.Close()
	if _, err := flow.PreviewCheckout(context.Background(), agentCheckout); err != nil {
		t.Fatal("empty error value rejected a valid preview", err)
	}
}

func TestCheckoutResponseShapeNeverEchoesSecretsOrReplays(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"checkout_url":"https://private.example/c/pay/private-token?cookie=private#private"}`, "external-host,/c/pay/token:true"},
		{`{"checkout_url":"https://invoice.stripe.com/i/private-token"}`, "invoice.stripe.com,/i/token:true"},
		{`{"invoice_url":"https://invoice.stripe.com/i/private-token"}`, "invoice_url=string"},
		{`{"url":"https://checkout.stripe.com/c/pay/private-token"}`, "url=string"},
		{`{"checkout_url":42,"private-key":"private-cookie"}`, "checkout_url=number"},
		{`{"error":{"code":"private-cookie"},"private-key":"private-value"}`, "error=object"},
		{`{"checkout_url":"https://private-user:private-password@checkout.stripe.com/c/pay/private-token"}`, "userinfo:true"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					w.Write([]byte(freeAgentQuote))
					return
				}
				w.Write([]byte(tc.body))
			}))
			defer up.Close()
			f, _ := New(Config{BaseURL: up.URL})
			defer f.Close()
			_, err := f.CreateCheckout(context.Background(), agentCheckout)
			failure := CheckoutFailureOf(err)
			if failure == nil || !failure.Uncertain || failure.Stage != "create" || !strings.Contains(failure.Message, tc.want) || calls != 2 {
				t.Fatalf("missing shape or replay: %+v calls=%d", failure, calls)
			}
			raw, _ := json.Marshal(failure)
			if strings.Contains(string(raw)+err.Error(), "private") {
				t.Fatal("response values/unknown keys leaked")
			}
		})
	}
	if checkoutResponseShape([]byte(`"private-cookie"`)) != "非JSON对象" {
		t.Fatal("non-object reflected")
	}
}

func TestCheckoutPreviewNeverCreatesHostedSession(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["preview"] != true || body["autoRenew"] != false || len(body) != 4 {
			t.Error("preview reached mutation payload", body)
		}
		w.Write([]byte(freeAgentQuote))
	}))
	defer up.Close()
	f, _ := New(Config{BaseURL: up.URL})
	defer f.Close()
	quote, err := f.PreviewCheckout(context.Background(), agentCheckout)
	if err != nil || quote == nil || quote.PayNowUSD == nil || *quote.PayNowUSD != .99 || calls != 1 {
		t.Fatal("read-only preview failed", err, calls)
	}
}
