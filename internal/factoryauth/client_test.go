package factoryauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func jwt(payload map[string]any) string {
	b, _ := json.Marshal(payload)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func TestDevicePollAndRefresh(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/authorize/device" {
			if r.FormValue("client_id") != "cid" {
				t.Error("client id", r.FormValue("client_id"))
			}
			json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "ABCD-EFGH", "verification_uri": "https://auth.example/device", "expires_in": 300, "interval": 1})
			return
		}
		if r.URL.Path != "/authenticate" {
			t.Fatal("unexpected path", r.URL.Path)
		}
		if r.FormValue("grant_type") == "refresh_token" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": "u1", "org_id": "org1"}), "active_organization_id": "org1"})
			return
		}
		polls++
		if polls == 1 {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": "u1", "email": "a@example.com", "org_id": "org1"}),
			"refresh_token": "refresh1", "active_organization_id": "org1",
		})
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, ClientID: "cid"}
	d, err := c.StartDevice(context.Background())
	if err != nil || d.DeviceCode != "dc" || d.Interval != 1 {
		t.Fatal("device", d, err)
	}
	if _, err := c.PollDevice(context.Background(), "dc"); !errors.Is(err, ErrPending) {
		t.Fatal("pending", err)
	}
	tok, err := c.PollDevice(context.Background(), "dc")
	if err != nil || tok.AccessToken == "" || tok.RefreshToken != "refresh1" || tok.UserID != "u1" || tok.Email != "a@example.com" || tok.OrgID != "org1" || tok.ExpiresAt.Before(time.Now()) {
		t.Fatal("token", tok, err)
	}
	next, err := c.Refresh(context.Background(), "refresh1", "org1")
	if err != nil || next.AccessToken == "" || next.RefreshToken != "refresh1" || next.OrgID != "org1" {
		t.Fatal("refresh", next, err)
	}
}

func TestDeniedAndInvalidRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "access_denied"})
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, ClientID: "cid"}
	if _, err := c.PollDevice(context.Background(), "dc"); !errors.Is(err, ErrDenied) {
		t.Fatal("denied", err)
	}
	if _, err := c.Refresh(context.Background(), "r", ""); !errors.Is(err, ErrDenied) {
		t.Fatal("invalid grant", err)
	}
}
