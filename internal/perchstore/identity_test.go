package perchstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/perch"
)

const identityTestTimeout = 10 * time.Second

func receiveWithin[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(identityTestTimeout):
		t.Fatal("timed out waiting for test event")
		var zero T
		return zero
	}
}

func releaseOnce() (chan struct{}, func()) {
	ch := make(chan struct{})
	var once sync.Once
	return ch, func() { once.Do(func() { close(ch) }) }
}

func testJWT(sub string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":%q}`, sub))) + ".signature"
}

func TestSessionIdentityOnPutAndBatch(t *testing.T) {
	k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{testJWT("u1"), "opaque-token", "e30.invalid.signature"} {
		if _, err := k.Put(Secrets{UserID: "u1", AccessToken: token}, PutOptions{Force: true}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := k.ByUserID("u1")
	if _, err := k.Put(Secrets{UserID: "u1", AccessToken: testJWT("u2")}, PutOptions{Force: true}); !errors.Is(err, ErrSessionIdentity) {
		t.Fatalf("Put error = %v", err)
	}
	_, err = k.PutMany([]Secrets{{UserID: "u3", AccessToken: testJWT("u3")}, {UserID: "u1", AccessToken: testJWT("u2")}}, []PutOptions{{}, {Force: true}})
	if !errors.Is(err, ErrSessionIdentity) {
		t.Fatalf("PutMany error = %v", err)
	}
	if _, ok := k.ByUserID("u3"); ok {
		t.Fatal("batch partially applied")
	}
	after, _ := k.ByUserID("u1")
	if after != before {
		t.Fatal("mismatched token replaced stored session")
	}
}

func TestRefreshDiscardsSupersededResult(t *testing.T) {
	for _, tc := range []struct {
		name             string
		refuse, recreate bool
	}{
		{"success", false, false}, {"refusal", true, false}, {"recreated", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan struct{})
			release, unblock := releaseOnce()
			var startedOnce sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				startedOnce.Do(func() { close(started) })
				select {
				case <-release:
				case <-time.After(identityTestTimeout):
					t.Error("timed out waiting to release handler")
					return
				}
				if tc.refuse {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, `{"error":"invalid_grant"}`)
					return
				}
				fmt.Fprint(w, `{"access_token":"old-answer","refresh_token":"old-rotated","user":{"id":"u1"}}`)
			}))
			defer func() { unblock(); srv.Close() }()
			k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			k.SetClient(&perch.Client{HTTP: srv.Client()})
			old := Secrets{UserID: "u1", SupabaseURL: srv.URL, AccessToken: "old-access", RefreshToken: "old-refresh", FetchedAt: time.Now()}
			if _, err := k.Put(old, PutOptions{BindAs: "acc", Force: true}); err != nil {
				t.Fatal(err)
			}
			done := make(chan RefreshResult, 1)
			go func() { done <- k.Refresh(context.Background(), "acc", true) }()
			receiveWithin(t, started)
			if tc.recreate {
				k.DropUser("u1")
			}
			next := old
			next.AccessToken = "new-access"
			next.RefreshToken = "new-refresh"
			if _, err := k.Put(next, PutOptions{BindAs: "acc", Force: true}); err != nil {
				t.Fatal(err)
			}
			unblock()
			res := receiveWithin(t, done)
			if !res.Skipped || res.Err != nil || res.NeedLogin {
				t.Fatalf("stale result = %+v", res)
			}
			got, _ := k.Get("acc")
			if got.AccessToken != "new-access" || got.RefreshToken != "new-refresh" {
				t.Fatalf("new session overwritten: %+v", got)
			}
			if !k.IsCurrentRefresh("acc", res) {
				t.Fatal("result does not describe current session")
			}
		})
	}
}

func TestRefreshFailureAfterMetadataChangeIsNotReportedAsSuccess(t *testing.T) {
	started := make(chan struct{})
	release, unblock := releaseOnce()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		select {
		case <-release:
		case <-time.After(identityTestTimeout):
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer srv.Close()
	defer unblock()
	k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	k.SetClient(&perch.Client{HTTP: srv.Client()})
	s := Secrets{UserID: "u1", SupabaseURL: srv.URL, AccessToken: "access", RefreshToken: "refresh"}
	if _, err := k.Put(s, PutOptions{BindAs: "acc", Force: true}); err != nil {
		t.Fatal(err)
	}
	done := make(chan RefreshResult, 1)
	go func() { done <- k.Refresh(context.Background(), "acc", true) }()
	receiveWithin(t, started)
	s.Email = "changed@example.invalid"
	if _, err := k.Put(s, PutOptions{BindAs: "acc", Force: true}); err != nil {
		t.Fatal(err)
	}
	unblock()
	res := receiveWithin(t, done)
	if res.Err == nil || res.Skipped || res.NeedLogin {
		t.Fatalf("superseded failure = %+v", res)
	}
}

func TestIncompleteImportPreservesCompleteSession(t *testing.T) {
	k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	original := Secrets{UserID: "u1", AccessToken: "access", RefreshToken: "refresh", FetchedAt: time.Now()}
	if _, err := k.Put(original, PutOptions{BindAs: "acc"}); err != nil {
		t.Fatal(err)
	}
	for _, partial := range []Secrets{
		{UserID: "u1", AccessToken: "new-access", FetchedAt: time.Now().Add(time.Minute)},
		{UserID: "u1", RefreshToken: "new-refresh", FetchedAt: time.Now().Add(time.Minute)},
	} {
		res, err := k.Put(partial, PutOptions{BindAs: "acc"})
		if err != nil || !res.Skipped {
			t.Fatalf("incomplete import = %+v, %v", res, err)
		}
		got, ok := k.Get("acc")
		if !ok || got.AccessToken != original.AccessToken || got.RefreshToken != original.RefreshToken {
			t.Fatal("incomplete import replaced a complete session")
		}
	}
}

func TestIncompleteImportBindsExistingSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		oldUID string
	}{
		{name: "unbound"},
		{name: "previously bound", oldUID: "u2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			original := Secrets{UserID: "u1", AccessToken: "access", RefreshToken: "refresh"}
			if _, err := k.Put(original, PutOptions{Force: true}); err != nil {
				t.Fatal(err)
			}
			if tc.oldUID != "" {
				k.Bind("acc", tc.oldUID)
			}
			res, err := k.Put(Secrets{UserID: "u1", AccessToken: "partial"}, PutOptions{BindAs: "acc"})
			if err != nil || !res.Skipped {
				t.Fatalf("incomplete import = %+v, %v", res, err)
			}
			got, ok := k.Get("acc")
			if !ok || got.UserID != original.UserID || got.AccessToken != original.AccessToken || got.RefreshToken != original.RefreshToken {
				t.Fatalf("binding did not retain existing session: %+v, exists=%v", got, ok)
			}
		})
	}
}

func TestLoadedSessionHasRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets")
	key := []byte("0123456789abcdef")
	k, err := Open(path, key, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Put(Secrets{UserID: "u1", AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).Unix()}, PutOptions{BindAs: "acc"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(path, key, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.Bind("acc", "u1")
	res := reloaded.Refresh(context.Background(), "acc", false)
	if !res.Skipped || res.Revision == 0 || !reloaded.IsCurrentRefresh("acc", res) {
		t.Fatalf("loaded result = %+v", res)
	}
}

func TestRefreshPreservesRotatedTokenAfterConcurrentChanges(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(*Keeper, Secrets, string)
		wantStored bool
		wantRotate bool
	}{
		{"same tokens", func(k *Keeper, s Secrets, _ string) {
			s.Email = "updated@example.invalid"
			_, _ = k.Put(s, PutOptions{BindAs: "acc", Force: true})
		}, true, true},
		{"access token changed", func(k *Keeper, s Secrets, _ string) {
			s.AccessToken = "new-access"
			_, _ = k.Put(s, PutOptions{BindAs: "acc", Force: true})
		}, true, true},
		{"refresh token changed", func(k *Keeper, s Secrets, _ string) {
			s.RefreshToken = "new-refresh"
			_, _ = k.Put(s, PutOptions{BindAs: "acc", Force: true})
		}, true, false},
		{"target changed", func(k *Keeper, s Secrets, _ string) {
			s.SupabaseURL = "https://other.invalid"
			_, _ = k.Put(s, PutOptions{BindAs: "acc", Force: true})
		}, true, false},
		{"key changed", func(k *Keeper, s Secrets, _ string) {
			s.SupabaseKey = "other-key"
			_, _ = k.Put(s, PutOptions{BindAs: "acc", Force: true})
		}, true, true},
		{"initiator unbound", func(k *Keeper, _ Secrets, _ string) {
			k.Bind("other", "u1")
			k.Forget("acc")
		}, true, true},
		{"last binding removed", func(k *Keeper, _ Secrets, _ string) {
			k.Forget("acc")
		}, false, false},
		{"deleted and recreated", func(k *Keeper, s Secrets, _ string) {
			k.DropUser("u1")
			_, _ = k.Put(s, PutOptions{BindAs: "acc", Force: true})
		}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan struct{})
			release, unblock := releaseOnce()
			var startedOnce sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				startedOnce.Do(func() { close(started) })
				select {
				case <-release:
				case <-time.After(identityTestTimeout):
					t.Error("timed out waiting to release handler")
					return
				}
				fmt.Fprint(w, `{"access_token":"rotated-access","refresh_token":"rotated-refresh","user":{"id":"u1"}}`)
			}))
			defer func() { unblock(); srv.Close() }()
			k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			s := Secrets{UserID: "u1", SupabaseURL: srv.URL, AccessToken: "access", RefreshToken: "refresh"}
			if _, err := k.Put(s, PutOptions{BindAs: "acc", Force: true}); err != nil {
				t.Fatal(err)
			}
			done := make(chan RefreshResult, 1)
			go func() { done <- k.Refresh(context.Background(), "acc", true) }()
			receiveWithin(t, started)
			tc.change(k, s, srv.URL)
			unblock()
			receiveWithin(t, done)
			got, exists := k.ByUserID("u1")
			if exists != tc.wantStored {
				t.Fatalf("record exists = %v, want %v", exists, tc.wantStored)
			}
			if exists && (got.RefreshToken == "rotated-refresh") != tc.wantRotate {
				t.Fatalf("rotated token stored = %v, want %v", got.RefreshToken == "rotated-refresh", tc.wantRotate)
			}
			if tc.name == "same tokens" && got.Email != "updated@example.invalid" {
				t.Fatal("concurrent metadata edit was lost")
			}
		})
	}
}

func TestRefreshJoinsInFlightAfterSameTokenUpdate(t *testing.T) {
	started := make(chan struct{})
	release, unblock := releaseOnce()
	var startedOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-time.After(identityTestTimeout):
			t.Error("timed out waiting to release handler")
			return
		}
		fmt.Fprint(w, `{"access_token":"rotated-access","refresh_token":"rotated-refresh","user":{"id":"u1"}}`)
	}))
	defer func() { unblock(); srv.Close() }()
	k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := Secrets{UserID: "u1", SupabaseURL: srv.URL, AccessToken: "access", RefreshToken: "refresh"}
	if _, err := k.Put(s, PutOptions{BindAs: "acc", Force: true}); err != nil {
		t.Fatal(err)
	}
	done := make(chan RefreshResult, 1)
	go func() { done <- k.Refresh(context.Background(), "acc", true) }()
	receiveWithin(t, started)
	if _, err := k.Put(s, PutOptions{BindAs: "acc", Force: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := k.Refresh(ctx, "acc", true); !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("joining canceled refresh returned %v", res.Err)
	}
	unblock()
	if res := receiveWithin(t, done); res.Err != nil || !res.Renewed {
		t.Fatalf("initial refresh failed: %v", res.Err)
	}
}

type signaledDoneContext struct {
	context.Context
	waiting chan struct{}
}

func (c signaledDoneContext) Done() <-chan struct{} {
	select {
	case <-c.waiting:
	default:
		close(c.waiting)
	}
	return c.Context.Done()
}

func TestRefreshWaiterRetriesChangedToken(t *testing.T) {
	started := make(chan struct{})
	release, unblock := releaseOnce()
	var startedOnce sync.Once
	calls := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		select {
		case calls <- body.RefreshToken:
		case <-time.After(identityTestTimeout):
			t.Error("timed out recording refresh token")
			return
		}
		if body.RefreshToken == "old-refresh" {
			startedOnce.Do(func() { close(started) })
			select {
			case <-release:
			case <-time.After(identityTestTimeout):
				t.Error("timed out waiting to release handler")
				return
			}
		}
		fmt.Fprint(w, `{"access_token":"rotated-access","refresh_token":"rotated-refresh","user":{"id":"u1"}}`)
	}))
	defer func() { unblock(); srv.Close() }()
	k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := Secrets{UserID: "u1", SupabaseURL: srv.URL, AccessToken: "access", RefreshToken: "old-refresh"}
	if _, err := k.Put(s, PutOptions{BindAs: "acc", Force: true}); err != nil {
		t.Fatal(err)
	}
	first := make(chan RefreshResult, 1)
	go func() { first <- k.Refresh(context.Background(), "acc", true) }()
	receiveWithin(t, started)
	s.RefreshToken = "new-refresh"
	if _, err := k.Put(s, PutOptions{BindAs: "acc", Force: true}); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan struct{})
	ctx := signaledDoneContext{Context: context.Background(), waiting: waiting}
	second := make(chan RefreshResult, 1)
	go func() { second <- k.Refresh(ctx, "acc", true) }()
	receiveWithin(t, waiting)
	unblock()
	receiveWithin(t, first)
	if res := receiveWithin(t, second); res.Err != nil || !res.Renewed {
		t.Fatalf("second refresh did not renew: %v", res.Err)
	}
	if old, next := receiveWithin(t, calls), receiveWithin(t, calls); old != "old-refresh" || next != "new-refresh" {
		t.Fatal("refresh calls did not use the expected token order")
	}
}

func TestRefreshWaiterReceivesErrorAfterInitiatorUnbound(t *testing.T) {
	started := make(chan struct{})
	release, unblock := releaseOnce()
	var startedOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-time.After(identityTestTimeout):
			t.Error("timed out waiting to release handler")
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"invalid_grant","error_code":"refresh_token_already_used"}`)
	}))
	defer func() { unblock(); srv.Close() }()
	k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := Secrets{UserID: "u1", SupabaseURL: srv.URL, AccessToken: "access", RefreshToken: "refresh"}
	if _, err := k.Put(s, PutOptions{BindAs: "acc", Force: true}); err != nil {
		t.Fatal(err)
	}
	k.Bind("other", "u1")
	first := make(chan RefreshResult, 1)
	go func() { first <- k.Refresh(context.Background(), "acc", true) }()
	receiveWithin(t, started)
	waiting := make(chan struct{})
	ctx := signaledDoneContext{Context: context.Background(), waiting: waiting}
	second := make(chan RefreshResult, 1)
	go func() { second <- k.Refresh(ctx, "other", true) }()
	receiveWithin(t, waiting)
	k.Forget("acc")
	unblock()
	if res := receiveWithin(t, first); !errors.Is(res.Err, ErrNoSession) {
		t.Fatalf("unbound account: %v", res.Err)
	}
	if res := receiveWithin(t, second); !res.NeedLogin || !errors.Is(res.Err, ErrNeedLogin) {
		t.Fatalf("bound account: %v", res.Err)
	}
}

func TestRefreshWaiterRechecksConfiguredKey(t *testing.T) {
	started := make(chan struct{})
	release, unblock := releaseOnce()
	var startedOnce sync.Once
	keys := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("apikey")
		select {
		case keys <- key:
		case <-time.After(identityTestTimeout):
			t.Error("timed out recording refresh key")
			return
		}
		if key == "old-key" {
			startedOnce.Do(func() { close(started) })
			select {
			case <-release:
			case <-time.After(identityTestTimeout):
				t.Error("timed out waiting to release handler")
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"invalid_grant","error_code":"refresh_token_already_used"}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","user":{"id":"u1"}}`)
	}))
	defer func() { unblock(); srv.Close() }()
	var key atomic.Value
	key.Store("old-key")
	cfg := func() Config { return Config{SupabaseKey: key.Load().(string)} }
	k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := Secrets{UserID: "u1", SupabaseURL: srv.URL, AccessToken: "access", RefreshToken: "refresh"}
	if _, err := k.Put(s, PutOptions{BindAs: "acc", Force: true}); err != nil {
		t.Fatal(err)
	}
	first := make(chan RefreshResult, 1)
	go func() { first <- k.Refresh(context.Background(), "acc", true) }()
	receiveWithin(t, started)
	key.Store("new-key")
	waiting := make(chan struct{})
	second := make(chan RefreshResult, 1)
	go func() {
		second <- k.Refresh(signaledDoneContext{Context: context.Background(), waiting: waiting}, "acc", true)
	}()
	receiveWithin(t, waiting)
	unblock()
	receiveWithin(t, first)
	if res := receiveWithin(t, second); res.Err != nil || !res.Renewed {
		t.Fatalf("new target did not renew: %v", res.Err)
	}
	if old, next := receiveWithin(t, keys), receiveWithin(t, keys); old != "old-key" || next != "new-key" {
		t.Fatal("refresh keys differed from expected order")
	}
}

func TestRefreshWaitsBeforeCheckingDue(t *testing.T) {
	started := make(chan struct{})
	release, unblock := releaseOnce()
	var startedOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-time.After(identityTestTimeout):
			t.Error("timed out waiting to release handler")
			return
		}
		fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","user":{"id":"u1"}}`)
	}))
	defer func() { unblock(); srv.Close() }()
	k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := Secrets{UserID: "u1", SupabaseURL: srv.URL, AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if _, err := k.Put(s, PutOptions{BindAs: "acc", Force: true}); err != nil {
		t.Fatal(err)
	}
	first := make(chan RefreshResult, 1)
	go func() { first <- k.Refresh(context.Background(), "acc", true) }()
	receiveWithin(t, started)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := k.Refresh(ctx, "acc", false); !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("did not wait for in-flight refresh: %v", res.Err)
	}
	unblock()
	receiveWithin(t, first)
}

func TestRefreshTargetUsesEffectiveFallbacks(t *testing.T) {
	cfg := Config{SupabaseKey: "fallback-key"}
	u, key := refreshTarget(Secrets{}, cfg)
	if u != perch.DefaultSupabaseURL || key != "fallback-key" {
		t.Fatal("default refresh target differs")
	}
	u, key = refreshTarget(Secrets{SupabaseURL: perch.DefaultSupabaseURL + "/auth/v1", SupabaseKey: " override "}, cfg)
	if u != perch.DefaultSupabaseURL || key != "override" {
		t.Fatal("explicit refresh target differs")
	}
}

func TestRefreshRejectsForeignIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"foreign","refresh_token":"foreign-refresh","user":{"id":"u2"}}`)
	}))
	defer srv.Close()
	k, err := Open(filepath.Join(t.TempDir(), "secrets"), []byte("0123456789abcdef"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	k.SetClient(&perch.Client{HTTP: srv.Client()})
	original := Secrets{UserID: "u1", SupabaseURL: srv.URL, AccessToken: "a", RefreshToken: "r"}
	if _, err := k.Put(original, PutOptions{BindAs: "acc"}); err != nil {
		t.Fatal(err)
	}
	res := k.Refresh(context.Background(), "acc", true)
	if !errors.Is(res.Err, ErrSessionIdentity) || res.NeedLogin {
		t.Fatalf("result = %+v", res)
	}
	got, _ := k.Get("acc")
	if got.UserID != "u1" || got.AccessToken != "a" {
		t.Fatalf("foreign session stored: %+v", got)
	}
}
