package x50signupstore

import (
	"path/filepath"
	"testing"
)

func TestOpenCreatesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "x50-signup.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	row, err := st.Add(Row{Email: "a@icloud.com", MailboxURL: "https://x/m?token=t", MailboxToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != Queued || row.ID == "" {
		t.Fatalf("row defaults: %+v", row)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(row.ID)
	if err != nil || got.Email != "a@icloud.com" {
		t.Fatalf("persisted row: %+v %v", got, err)
	}
	if reopened.Settings().Concurrency != DefaultSettings().Concurrency {
		t.Fatal("default settings lost on reopen")
	}
}

func TestDuplicateEmailRejectedCaseInsensitive(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(Row{Email: "A@iCloud.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(Row{Email: "a@icloud.com"}); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestUpdateRollsBackOnCallbackError(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	row, _ := st.Add(Row{Email: "b@icloud.com"})
	wantErr := "nope"
	if _, err := st.Update(row.ID, func(r *Row) error {
		r.APIKey = "should-not-persist"
		return &testErr{wantErr}
	}); err == nil {
		t.Fatal("callback error swallowed")
	}
	after, _ := st.Get(row.ID)
	if after.APIKey != "" {
		t.Fatal("failed update leaked into the row")
	}
}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }

func TestUpdateRollsBackWhenPersistFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	row, _ := st.Add(Row{Email: "c@icloud.com"})
	// Make the directory read-only so the atomic rename cannot land.
	if err := removeWrite(dir); err != nil {
		t.Skip("cannot drop write permission here:", err)
	}
	t.Cleanup(func() { _ = addWrite(dir) })
	if _, err := st.Update(row.ID, func(r *Row) error {
		r.Status = Keyed
		return nil
	}); err == nil {
		t.Skip("write still succeeded; filesystem ignores the permission bit")
	}
	if got, _ := st.Get(row.ID); got.Status == Keyed {
		t.Fatal("in-memory row advanced despite a failed persist")
	}
}

func TestDeleteAndClearFinished(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := st.Add(Row{Email: "a@i.com", Status: Keyed})
	b, _ := st.Add(Row{Email: "b@i.com", Status: Failed})
	c, _ := st.Add(Row{Email: "c@i.com", Status: Queued})

	if err := st.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(a.ID); err == nil {
		t.Fatal("deleted row still readable")
	}
	// re-add the finished row to exercise ClearFinished
	if _, err := st.Add(Row{Email: "d@i.com", Status: Keyed}); err != nil {
		t.Fatal(err)
	}
	n, err := st.ClearFinished()
	if err != nil || n != 1 {
		t.Fatalf("clear finished: %d %v", n, err)
	}
	left := st.List()
	if len(left) != 2 {
		t.Fatalf("left %d rows: %+v", len(left), left)
	}
	for _, r := range left {
		if r.ID != b.ID && r.ID != c.ID {
			t.Fatalf("unexpected survivor %+v", r)
		}
	}
}

func TestListIsNewestFirstWithStableTieBreak(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := st.Add(Row{Email: string(rune('a'+i)) + "@i.com"}); err != nil {
			t.Fatal(err)
		}
	}
	list := st.List()
	if len(list) != 5 {
		t.Fatalf("len %d", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].CreatedAt.Before(list[i].CreatedAt) {
			t.Fatalf("not newest-first at %d", i)
		}
	}
}

func TestSettingsNormalizationAndBounds(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := st.SaveSettings(Settings{Concurrency: 0, KeyName: ""})
	if err != nil {
		t.Fatal(err)
	}
	if out.Concurrency != DefaultSettings().Concurrency || out.KeyName != "registrar" {
		t.Fatalf("zero values not normalized: %+v", out)
	}
	if _, err := st.SaveSettings(Settings{Concurrency: MaxConcurrency + 1}); err == nil {
		t.Fatal("over-max concurrency accepted")
	}
	// a low value is clamped, not rejected
	out, err = st.SaveSettings(Settings{Concurrency: -3})
	if err != nil || out.Concurrency != MinConcurrency {
		t.Fatalf("clamp: %+v %v", out, err)
	}
	out, err = st.SaveSettings(Settings{Concurrency: 4, ProxyPool: []string{"socks5://a"}, PasswordMode: "weird"})
	if err != nil || out.Concurrency != 4 || out.PasswordMode != "random" || len(out.ProxyPool) != 1 {
		t.Fatalf("save: %+v %v", out, err)
	}
	// Settings() must hand out a private copy so callers cannot mutate the store
	read := st.Settings()
	read.ProxyPool[0] = "mutated"
	if st.Settings().ProxyPool[0] != "socks5://a" {
		t.Fatal("settings slice aliased")
	}
}

func TestAddManyReportsDuplicatesWithoutPartialMutation(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(Row{Email: "x@i.com"}); err != nil {
		t.Fatal(err)
	}
	added, errs, err := st.AddMany([]Row{
		{Email: "x@i.com"},
		{Email: "y@i.com"},
		{Email: ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0].Email != "y@i.com" {
		t.Fatalf("added %+v", added)
	}
	if len(errs) != 2 {
		t.Fatalf("errors %v", errs)
	}
}

func TestCorruptStoreIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	if err := writeFile(path, "{not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("corrupt store loaded")
	}
}
