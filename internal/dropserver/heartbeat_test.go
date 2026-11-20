package dropserver

import (
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"sync"
	"testing"
	"time"
)

// Reports update the same heartbeat that an account reads through app status.
func TestConcurrentHeartbeatStatus(t *testing.T) {
	node := core.NewRecord(core.NewAuthCollection("nodes"))
	node.Id = "node"
	state := newAgentState()
	start := time.Now()
	state.seen(node.Id, "same", start)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 1; i <= 1000; i++ {
			state.seen(node.Id, "same", start.Add(time.Duration(i)))
		}
	})
	for range 1000 {
		if state.lastSeen(node).Before(start) {
			t.Fatal("heartbeat moved backwards")
		}
	}
	wg.Wait()
	if got := state.lastSeen(node); !got.Equal(start.Add(1000)) {
		t.Errorf("last heartbeat %v", got)
	}
}
