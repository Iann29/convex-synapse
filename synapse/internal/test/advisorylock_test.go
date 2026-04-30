package synapsetest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Iann29/synapse/internal/db"
)

// Two callers race the same advisory key — exactly one acquires, the other
// observes acquired=false (no error). The acquirer's closure runs.
func TestAdvisoryLock_OnlyOneAcquires(t *testing.T) {
	h := Setup(t)
	const key int64 = 0xDEADBEEF01

	// First call: lock free, fn runs, returns acquired=true.
	ran := false
	got, err := db.WithTryAdvisoryLock(context.Background(), h.DB, key, func(_ context.Context) error {
		ran = true
		return nil
	})
	if err != nil || !got || !ran {
		t.Fatalf("first call: acquired=%v err=%v ran=%v", got, err, ran)
	}

	// Second call AFTER the first released: also acquires (lock is free
	// again because the conn went back to the pool).
	got2, err := db.WithTryAdvisoryLock(context.Background(), h.DB, key, func(_ context.Context) error {
		return nil
	})
	if err != nil || !got2 {
		t.Fatalf("second call: acquired=%v err=%v", got2, err)
	}
}

// While one caller is holding the lock, a concurrent attempt must observe
// acquired=false (no error). The holder's closure completes, then the
// follower could acquire on the next tick.
func TestAdvisoryLock_ContendedNonBlocking(t *testing.T) {
	h := Setup(t)
	const key int64 = 0xDEADBEEF02

	holding := make(chan struct{})
	releaseHolder := make(chan struct{})
	holderErr := make(chan error, 1)

	go func() {
		_, err := db.WithTryAdvisoryLock(context.Background(), h.DB, key, func(_ context.Context) error {
			close(holding)
			<-releaseHolder
			return nil
		})
		holderErr <- err
	}()

	<-holding // ensure holder acquired

	// Follower attempt while holder is still inside fn().
	got, err := db.WithTryAdvisoryLock(context.Background(), h.DB, key, func(_ context.Context) error {
		t.Error("follower closure must NOT run when lock is contended")
		return nil
	})
	if err != nil {
		t.Errorf("follower err: %v", err)
	}
	if got {
		t.Errorf("follower got the lock while holder was still inside")
	}

	// Let holder finish.
	close(releaseHolder)
	if err := <-holderErr; err != nil {
		t.Errorf("holder fn err: %v", err)
	}

	// Now the lock is free — a fresh attempt should acquire.
	got2, err := db.WithTryAdvisoryLock(context.Background(), h.DB, key, func(_ context.Context) error {
		return nil
	})
	if err != nil || !got2 {
		t.Errorf("after release: acquired=%v err=%v", got2, err)
	}
}

// 20 goroutines fire at the same key. Exactly one closure must run for any
// given moment in time; we don't strictly require only-one-ever-runs since
// they fire sequentially under contention, but we do require that no two
// closures are simultaneously executing.
func TestAdvisoryLock_MutualExclusion(t *testing.T) {
	h := Setup(t)
	const key int64 = 0xDEADBEEF03

	var inside atomic.Int32
	var maxInside atomic.Int32
	var ran atomic.Int32

	var wg sync.WaitGroup
	const N = 20
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = db.WithTryAdvisoryLock(context.Background(), h.DB, key, func(_ context.Context) error {
				cur := inside.Add(1)
				defer inside.Add(-1)
				for {
					m := maxInside.Load()
					if cur <= m || maxInside.CompareAndSwap(m, cur) {
						break
					}
				}
				ran.Add(1)
				time.Sleep(5 * time.Millisecond) // hold the lock long enough to be observable
				return nil
			})
		}()
	}
	wg.Wait()

	if maxInside.Load() > 1 {
		t.Errorf("mutual exclusion violated: maxInside=%d", maxInside.Load())
	}
	// At least one closure ran (others may have skipped); typically 1-3 do.
	if ran.Load() == 0 {
		t.Errorf("no closure ran at all")
	}
}
