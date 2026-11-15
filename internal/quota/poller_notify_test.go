package quota

import (
	"testing"

	"account-pool-balancer/internal/config"
)

func TestForgetNotifiesSnapshotConsumersOutsideLock(t *testing.T) {
	cfg := config.Default()
	p := NewPoller(func() *config.Config { return cfg }, nil)
	p.Load(map[string]*Snapshot{"a": {Catalog: map[string]bool{"m": true}}})
	updates := 0
	p.OnUpdate = func(id string) {
		updates++
		if id != "a" || p.View(id) != nil {
			t.Fatal("Forget notified before dropping the snapshot")
		}
	}
	p.Forget("a")
	p.Forget("a")
	if updates != 1 {
		t.Fatalf("Forget notifications = %d, want 1", updates)
	}
}
