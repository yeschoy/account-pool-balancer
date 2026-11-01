package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perchstore"
)

// aliases keep the test readable
type perchstoreResult = perchstore.RefreshResult
type configAccount = config.Account

// The background scheduler is what makes "import once, keep working" true
// without an operator touching anything. This drives it the way the server
// does and proves it renews the due sessions and nothing else.
func TestPerchSchedulerRenewsDueSessions(t *testing.T) {
	e := newPerchEnv(t)
	// one token already spent, one still fresh
	e.fake.seed("due-one")
	e.fake.seed("fresh-one")
	due := exportDoc("due-one", e.fake, time.Now().Add(-time.Minute).Unix(), 0)
	fresh := exportDoc("fresh-one", e.fake, time.Now().Add(time.Hour).Unix(), 0)
	e.importFiles(map[string][]byte{"due.json": []byte(due), "fresh.json": []byte(fresh)})

	var renewed atomic.Int64
	e.srv.perch.sched.OnResult = func(id string, res perchstoreResult) {
		if res.Renewed {
			renewed.Add(1)
		}
	}
	n := e.srv.perch.sched.RefreshDue(context.Background())
	if n != 1 {
		t.Fatalf("one session is due, scheduler attempted %d", n)
	}
	if got := renewed.Load(); got != 1 {
		t.Fatalf("renewed %d sessions, want 1", got)
	}
	if e.fake.refreshCount("due-one") != 1 {
		t.Fatalf("the due account was refreshed %d times", e.fake.refreshCount("due-one"))
	}
	if e.fake.refreshCount("fresh-one") != 0 {
		t.Fatal("a fresh session was refreshed")
	}
	// the rotated token is what the pool now holds
	_, rows := e.accounts("q=due-one")
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	sec, ok := e.srv.perch.keeper.Get(rows[0]["id"].(string))
	if !ok || sec.RefreshToken != "refresh-due-one-1" {
		t.Fatalf("the rotated token was not kept: %+v", sec)
	}
	// and a second pass finds nothing due
	if n := e.srv.perch.sched.RefreshDue(context.Background()); n != 0 {
		t.Fatalf("a second cycle attempted %d, want 0", n)
	}
}

// One account whose token is dead must not stop the others renewing in the
// same cycle.
func TestPerchSchedulerIsolatesFailures(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("ok-1")
	e.fake.seed("dead-1")
	e.fake.seed("ok-2")
	docs := map[string][]byte{
		"a.json": []byte(exportDoc("ok-1", e.fake, time.Now().Add(-time.Minute).Unix(), 0)),
		"b.json": []byte(exportDoc("dead-1", e.fake, time.Now().Add(-time.Minute).Unix(), 0)),
		"c.json": []byte(exportDoc("ok-2", e.fake, time.Now().Add(-time.Minute).Unix(), 0)),
	}
	e.importFiles(docs)
	// make only dead-1 refuse: it calls with its own token, so the fake
	// refuses everything while we do the run and we check the others still move
	e.fake.refuse.Store(false)

	// pretend dead-1's token is spent by rotating it out from under the store
	e.fake.mu.Lock()
	e.fake.live["dead-1"] = "someone-else-rotated-it"
	e.fake.mu.Unlock()

	e.srv.perch.sched.RefreshBlocks(context.Background(), e.accountIDs())

	// the two good ones renewed; the dead one is marked, not silently kept
	a1 := e.findByUser("ok-1")
	a2 := e.findByUser("ok-2")
	if a1.PerchRefreshFailed || a2.PerchRefreshFailed {
		t.Fatal("a failure elsewhere marked a healthy account")
	}
	dead := e.findByUser("dead-1")
	if !dead.PerchRefreshFailed {
		t.Fatal("the refused account was not marked")
	}
	if e.fake.refreshCount("ok-1") != 1 || e.fake.refreshCount("ok-2") != 1 {
		t.Fatal("a healthy account did not renew")
	}
}

func (e *perchEnv) accountIDs() []string {
	var ids []string
	for _, a := range e.srv.cfg.Get().Accounts {
		ids = append(ids, a.ID)
	}
	return ids
}

func (e *perchEnv) findByUser(uid string) configAccount {
	for _, a := range e.srv.cfg.Get().Accounts {
		if a.PerchUserID == uid {
			return a
		}
	}
	e.t.Fatalf("no account for %s", uid)
	return configAccount{}
}
