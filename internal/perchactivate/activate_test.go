package perchactivate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestActivateFlow(t *testing.T) {
	session, _ := json.Marshal(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "user": strings.Repeat("x", 5000)})
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/rest/v1/rpc/perch_ai_select_starter_with_data_notice" {
			if r.Header.Get("Authorization") != "Bearer new-access" || r.Header.Get("apikey") != "test-key" || r.Header.Get("Cookie") != "" {
				t.Error("RPC credentials incorrect")
			}
			var v map[string]any
			_ = json.NewDecoder(r.Body).Decode(&v)
			if v["p_notice_version"] != "test-policy" {
				t.Error("policy missing")
			}
			fmt.Fprint(w, `{"ok":true,"plan_code":"pilot"}`)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("Bearer sent to web endpoint")
		}
		var value string
		for _, c := range r.Cookies() {
			if strings.HasPrefix(c.Name, "sb-127-auth-token.") {
				value += c.Value
			}
		}
		if !strings.HasPrefix(value, "base64-") {
			t.Error("missing chunked cookie")
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "base64-"))
		if err != nil || string(raw) != string(session) {
			t.Error("cookie differs from refreshed session")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "session-context"):
			fmt.Fprint(w, `{"context":{"tierSelectionRequired":true}}`)
		case strings.HasSuffix(r.URL.Path, "threads") && r.Method == "GET":
			fmt.Fprint(w, `{"threads":[]}`)
		case strings.HasSuffix(r.URL.Path, "threads") && r.Method == "POST":
			fmt.Fprint(w, `{"thread":{"id":"abc123456789"}}`)
		case strings.HasSuffix(r.URL.Path, "chat"):
			var v map[string]any
			_ = json.NewDecoder(r.Body).Decode(&v)
			if v["message"] != "hi" || v["threadId"] != "abc123456789" || v["webSearch"] != false {
				t.Error("invalid chat request")
			}
			fmt.Fprint(w, "data: {\"type\":\"answer_delta\",\"text\":\"hello\"}\n\ndata: {\"type\":\"done\",\"ok\":true}\n\n")
		default:
			t.Errorf("unexpected request: %s", r.URL)
		}
	}))
	defer server.Close()
	cfg := Config{HTTPClient: server.Client(), AppURL: server.URL, ChatURL: server.URL, SupabaseURL: server.URL, SupabaseKey: "test-key", PolicyVersion: "test-policy"}
	got, err := Activate(context.Background(), cfg, session)
	if err != nil || !got.Sent || !got.SelectedStarter || got.Already || got.PlanCode != "pilot" || got.ThreadID != "abc123456789" {
		t.Fatalf("result %#v error %v", got, err)
	}
	if len(calls) != 5 {
		t.Fatalf("calls: %v", calls)
	}
}

func TestConversationChecks(t *testing.T) {
	cases := []struct {
		name, list, messages string
		status               int
		wantAlready, wantErr bool
	}{
		{"existing", `{"threads":[{"id":"one & two"}]}`, `{"messages":[{"role":"assistant"},{"role":"user"}]}`, 200, true, false},
		{"message HTTP failure", `{"threads":[{"id":"a"}]}`, `{"error":"secret-token"}`, 500, false, true},
		{"invalid messages", `{"threads":[{"id":"a"}]}`, `{}`, 200, false, true},
		{"invalid threads", `{}`, ``, 200, false, true},
		{"missing ID", `{"threads":[{}]}`, ``, 200, false, true},
		{"missing role", `{"threads":[{"id":"a"}]}`, `{"messages":[{}]}`, 200, false, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "session-context"):
					fmt.Fprint(w, `{"context":{"planCode":"pro"}}`)
				case r.Method == "POST":
					t.Error("unexpected write")
				case r.URL.Query().Has("threadId"):
					if tt.name == "existing" && r.URL.Query().Get("threadId") != "one & two" {
						t.Error("thread ID not escaped")
					}
					w.WriteHeader(tt.status)
					fmt.Fprint(w, tt.messages)
				default:
					fmt.Fprint(w, tt.list)
				}
			}))
			defer server.Close()
			got, err := Activate(context.Background(), Config{AppURL: server.URL, ChatURL: server.URL, SupabaseURL: server.URL}, json.RawMessage(`{"access_token":"secret-token"}`))
			if (err != nil) != tt.wantErr || got.Already != tt.wantAlready {
				t.Fatalf("got %#v, %v", got, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-token") {
				t.Fatal("token leaked")
			}
		})
	}
}

func TestSSE(t *testing.T) {
	for _, tt := range []struct {
		input string
		ok    bool
	}{
		{"data: {\"type\":\"done\",\"ok\":true}\n\n", true},
		{"data: {\"type\":\"done\",\"ok\":false}\n\n", false},
		{"data: {\"type\":\"answer_delta\"}\n\n", false},
		{"data: nonsense\n\n", false},
		{"data: {\"type\":\"done\",\n" + "data: \"ok\":true}\n\n", true},
		{"data: {\"type\":\"done\",\"ok\":true}\n\ndata: {\"type\":\"done\",\"ok\":false}\n\n", false},
	} {
		if err := readDone(strings.NewReader(tt.input)); (err == nil) != tt.ok {
			t.Errorf("%q: %v", tt.input, err)
		}
	}
}

func TestCookieRotationAndRedirect(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprint(redirect), func(t *testing.T) {
			visited := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { visited = true }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if redirect {
					http.Redirect(w, r, target.URL, http.StatusFound)
					return
				}
				http.SetCookie(w, &http.Cookie{Name: "sb-127-auth-token.0", Value: "rotated"})
				fmt.Fprint(w, `{"context":{"planCode":"pilot"}}`)
			}))
			defer server.Close()
			jar, _ := cookiejar.New(nil)
			u, _ := url.Parse(server.URL)
			jar.SetCookies(u, []*http.Cookie{{Name: "injected", Value: "bad"}})
			cfg := Config{HTTPClient: &http.Client{Jar: jar}, AppURL: server.URL, ChatURL: server.URL, SupabaseURL: server.URL}
			_, err := Activate(context.Background(), cfg, json.RawMessage(`{"access_token":"secret-token"}`))
			if err == nil || visited || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("error %v, visited %v", err, visited)
			}
		})
	}
}

type errorTransport struct{}

func (errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("secret-token")
}
func TestTransportErrorRedacted(t *testing.T) {
	_, err := Activate(context.Background(), Config{HTTPClient: &http.Client{Transport: errorTransport{}}}, json.RawMessage(`{"access_token":"secret-token"}`))
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestSSEReadFailure(t *testing.T) {
	if err := readDone(io.MultiReader(strings.NewReader("data: {\"type\":\"done\",\"ok\":true}\n\n"), errReader{})); err == nil {
		t.Fatal("read failure ignored")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, fmt.Errorf("failure") }
