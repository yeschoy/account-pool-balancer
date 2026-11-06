package thsignupstore

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestAttemptNotesPersistWithoutChangingRows(t *testing.T) {
	s, path := openTemp(t)
	rows, err := s.AddSaved("task-one", []Row{
		{Email: "one@mock.test", Password: "private-password", ProxyURL: "http://u:pw@proxy.test:8080"},
		{Email: "two@mock.test", Password: "other-password"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSaved("task-two", []Row{{Email: "github:mock-user", GitHub: &GitHubAuth{Login: "mock-user"}}}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(rows[0].ID)
	// Metadata may be edited while work is running, without invalidating the
	// registration worker's UpdatedAt/source snapshot.
	if _, err := s.Update(before.ID, func(r *Row) error { r.Status = Running; return nil }); err != nil {
		t.Fatal(err)
	}
	before, _ = s.Get(before.ID)
	note, err := s.SetAttemptNote("task-one", "  美国代理批次 <note>  ")
	if err != nil || note != "美国代理批次 <note>" {
		t.Fatalf("save note: %q, %v", note, err)
	}
	after, _ := s.Get(before.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("task note changed row credentials, state or UpdatedAt")
	}
	if s.AttemptNote("task-two") != "" {
		t.Fatal("note crossed task boundary")
	}
	if _, err := s.SetAttemptNote("task-two", "GitHub测试批次"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProxies([]string{"http://saved-proxy.test:8080"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(before.ID, func(r *Row) error { r.Status = Saved; r.Detail = "checkpoint"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSaved("task-three", []Row{{Email: "three@mock.test"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"美国代理批次", "GitHub测试批次", "private-password"} {
		if strings.Contains(string(raw), text) {
			t.Fatal("encrypted metadata or email credential appeared in plaintext")
		}
	}
	reopened, err := Open(path, secret)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.AttemptNote("task-one") != note || reopened.AttemptNote("task-two") != "GitHub测试批次" {
		t.Fatal("notes lost across row/proxy writes or reopen")
	}
	if err := reopened.Delete(rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if reopened.AttemptNote("task-one") != note {
		t.Fatal("deleting one account erased the task note")
	}
	if err := reopened.Delete(rows[1].ID); err != nil {
		t.Fatal(err)
	}
	if reopened.AttemptNote("task-one") != "" || reopened.AttemptNote("task-two") == "" {
		t.Fatal("last-row deletion did not remove only its own task note")
	}
	reopened, err = Open(path, secret)
	if err != nil || reopened.AttemptNote("task-one") != "" {
		t.Fatal("deleted note returned after reopen", err)
	}
	if _, err := reopened.SetAttemptNote("task-two", " \n\t "); err != nil {
		t.Fatal(err)
	}
	reopened, err = Open(path, secret)
	if err != nil || reopened.AttemptNote("task-two") != "" {
		t.Fatal("clear did not persist", err)
	}
}

func TestAttemptNoteValidationAndFailedSaveAreAtomic(t *testing.T) {
	s, path := openTemp(t)
	if _, err := s.AddSaved("task", []Row{{Email: "legacy@mock.test"}}); err != nil {
		t.Fatal(err)
	}
	// A version-1 file without attemptNotes is still readable.
	legacy, err := Open(path, secret)
	if err != nil || legacy.AttemptNote("task") != "" {
		t.Fatal("legacy file rejected", err)
	}
	boundary := strings.Repeat("测", MaxAttemptNoteLength-1) + "😀"
	if _, err := s.SetAttemptNote("task", boundary); err != nil {
		t.Fatal("Unicode character boundary rejected", err)
	}
	before, _ := os.ReadFile(path)
	for _, note := range []string{boundary + "x", "bad\xff"} {
		if _, err := s.SetAttemptNote("task", note); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid note accepted", err)
		}
	}
	if _, err := s.SetAttemptNote("missing", "orphan"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown task accepted", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || s.AttemptNote("task") != boundary {
		t.Fatal("invalid edit changed persisted or in-memory note")
	}
	s.path = filepath.Join(path, "not-a-directory.json")
	if _, err := s.SetAttemptNote("task", "must not commit"); err == nil {
		t.Fatal("expected persistence failure")
	}
	if s.AttemptNote("task") != boundary {
		t.Fatal("failed save changed in-memory note")
	}
}

func TestAttemptNotesSerializeWithRowUpdates(t *testing.T) {
	s, path := openTemp(t)
	rows, err := s.AddSaved("task", []Row{{Email: "concurrent@mock.test"}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			if _, err := s.SetAttemptNote("task", "并发备注"); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := s.Update(rows[0].ID, func(r *Row) error { r.Detail = "saved checkpoint"; return nil }); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := s.SaveProxies([]string{"http://concurrent-proxy.test:8080"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	reopened, err := Open(path, secret)
	if err != nil {
		t.Fatal(err)
	}
	row, _ := reopened.Get(rows[0].ID)
	if reopened.AttemptNote("task") != "并发备注" || row.Detail != "saved checkpoint" || !reflect.DeepEqual(reopened.Proxies(), []string{"http://concurrent-proxy.test:8080"}) {
		t.Fatal("concurrent metadata, checkpoint or proxy writes lost data")
	}
}
