package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"account-pool-balancer/internal/config"
)

// fakePerchSwitch serves PATCH /api/perchai/data-program and records what it
// was sent, so the test asserts on the wire shape rather than on a mock.
type fakePerchSwitch struct {
	mu     sync.Mutex
	calls  int
	method string
	path   string
	bearer string
	body   map[string]any
	status int
	reply  string
}

func (f *fakePerchSwitch) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	f.method, f.path = r.Method, r.URL.Path
	f.bearer = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	_ = json.NewDecoder(r.Body).Decode(&f.body)
	status, reply := f.status, f.reply
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
	}
	_, _ = w.Write([]byte(reply))
}

// newSwitchEnv builds a server whose Perch client reaches f.
func newSwitchEnv(t *testing.T, f *fakePerchSwitch) *Server {
	t.Helper()
	fake := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(fake.Close)
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	hc := fake.Client()
	hc.Transport = &rewriteTransport{target: fake.Listener.Addr().String()}
	s.perch.keeper.SetClient(perchClientFor(hc))
	_, err = s.cfg.Update(-1, func(c *config.Config) error {
		c.Perch.AppURL = "https://app.perchai.app"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// strictSession is one exported document from a freshly activated account.
func strictSession() []byte {
	doc := exportDocAsMap("privacy")
	doc["supabase_session"] = doc["session"]
	raw, _ := json.Marshal(doc)
	return raw
}

func TestPerchDataProgramSwitchIsOff(t *testing.T) {
	f := &fakePerchSwitch{reply: `{"ok":true}`}
	s := newSwitchEnv(t, f)
	if err := s.disablePerchDataProgram(context.Background(), "", strictSession()); err != nil {
		t.Fatalf("switch refused: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 1 || f.method != http.MethodPatch || f.path != "/api/perchai/data-program" {
		t.Fatalf("wrong call: %d %s %s", f.calls, f.method, f.path)
	}
	if f.bearer != "access-privacy" {
		t.Fatalf("bearer = %q", f.bearer)
	}
	if enabled, ok := f.body["enabled"].(bool); !ok || enabled {
		t.Fatalf("enabled = %v", f.body["enabled"])
	}
	// The existing-data notice is never acknowledged: nothing showed it.
	if ack, present := f.body["acknowledgeExistingNotice"]; !present || ack != false {
		t.Fatalf("acknowledgeExistingNotice = %v (present %v)", ack, present)
	}
}

// A refusal is an error even when it arrives as a 2xx with ok:false.
func TestPerchDataProgramSwitchReportsRefusal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		reply  string
	}{
		{"ok:false on 200", 0, `{"ok":false,"errorCode":"banned"}`},
		{"401", http.StatusUnauthorized, `{}`},
		{"5xx", http.StatusBadGateway, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakePerchSwitch{status: tc.status, reply: tc.reply}
			if err := newSwitchEnv(t, f).disablePerchDataProgram(context.Background(), "", strictSession()); err == nil {
				t.Fatal("a refused or failed switch must be reported")
			}
		})
	}
}

// A session with no access token is not sent anywhere.
func TestPerchDataProgramRequiresToken(t *testing.T) {
	f := &fakePerchSwitch{reply: `{"ok":true}`}
	s := newSwitchEnv(t, f)
	var doc map[string]any
	if err := json.Unmarshal(strictSession(), &doc); err != nil {
		t.Fatal(err)
	}
	// Blank the token the switch reads: the envelope's supabase_session.
	delete(doc["supabase_session"].(map[string]any), "access_token")
	raw, _ := json.Marshal(doc)
	if err := s.disablePerchDataProgram(context.Background(), "", raw); err == nil {
		t.Fatal("missing token must not be attempted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 0 {
		t.Fatal("a tokenless session reached the network")
	}
}
