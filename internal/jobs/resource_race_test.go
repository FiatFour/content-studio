package jobs_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// Claims fired at the same instant must still hand out only the one GPU slot. Without the
// per-resource lock, several of them count zero running jobs and all take it.
func TestSimultaneousClaimsTakeOnlyOneGPUSlot(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	for round := 0; round < 15; round++ {
		if _, err := db.Exec(context.Background(), `UPDATE jobs SET state = 'completed', lease_owner = NULL, lease_expires_at = NULL WHERE state IN ('running', 'waiting_for_resource', 'queued')`); err != nil {
			t.Fatal(err)
		}
		seed := gpuQueue(db, "seed")
		for i := 0; i < 8; i++ {
			if _, err := seed.CreateRun(context.Background(), ep, "full", nil); err != nil {
				t.Fatal(err)
			}
		}
		var got atomic.Int32
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			q := gpuQueue(db, fmt.Sprintf("c%d", i))
			go func() {
				defer wg.Done()
				<-start
				if _, err := q.Claim(context.Background()); err == nil {
					got.Add(1)
				} else if err != jobs.ErrNoJob {
					t.Error(err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if got.Load() != 1 {
			t.Fatalf("round %d: %d claims got the single GPU slot", round, got.Load())
		}
	}
}
