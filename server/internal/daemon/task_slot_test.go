package daemon

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func newTestTaskSlot(index int) *taskSlotLease {
	return newTaskSlotLease(newTaskSlotSemaphore(newTestSlotDir(), index+1, nil), index, nil)
}

// AG patch: slots are flock files under a temp dir, not a channel.
func newTestSlotDir() string {
	dir, err := os.MkdirTemp("", "agslots")
	if err != nil {
		panic(err)
	}
	return dir
}

func newTestSlots(t *testing.T, n int) *agTaskSlots {
	t.Helper()
	return newTaskSlotSemaphore(t.TempDir(), n, nil)
}

func takeSlot(t *testing.T, s *agTaskSlots) int {
	t.Helper()
	slot, ok, err := s.tryAcquire()
	if !ok || err != nil {
		t.Fatalf("no free slot: ok=%v err=%v", ok, err)
	}
	return slot
}

func freeSlots(s *agTaskSlots) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n - len(s.held)
}

func TestTaskSlotLeaseReleaseIsIdempotent(t *testing.T) {
	t.Parallel()
	slots := newTestSlots(t, 1)
	wakeup := make(chan struct{}, 1)
	lease := newTaskSlotLease(slots, takeSlot(t, slots), wakeup)
	lease.release()
	if got := freeSlots(slots); got != 1 {
		t.Fatalf("free slots = %d, want 1", got)
	}
	select {
	case <-wakeup:
	default:
		t.Fatal("returning capacity did not wake the poller")
	}
	if got := takeSlot(t, slots); got != 0 {
		t.Fatalf("returned index = %d, want 0", got)
	}
	lease.release()
	if got := freeSlots(slots); got != 0 {
		t.Fatalf("second release duplicated the occupied slot: %d available", got)
	}
}

func TestTaskSlotLeaseReacquiresCurrentIndex(t *testing.T) {
	t.Parallel()
	slots := newTestSlots(t, 2)
	lease := newTaskSlotLease(slots, takeSlot(t, slots), nil)
	spare := takeSlot(t, slots)
	lease.release()
	other := takeSlot(t, slots)
	slots.release(spare)
	if err := lease.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lease.index != spare || lease.index == other {
		t.Fatalf("reacquired index = %d, want free %d while %d is occupied", lease.index, spare, other)
	}
	if err := lease.acquire(context.Background()); err != nil {
		t.Fatalf("already held slot: %v", err)
	}
	lease.release()
	slots.release(other)
	seen := map[int]bool{}
	for range 2 {
		index := takeSlot(t, slots)
		if seen[index] {
			t.Fatalf("returned duplicate slot %d", index)
		}
		seen[index] = true
	}
}

func TestTaskSlotLeaseCancelledAcquireConservesCapacity(t *testing.T) {
	t.Parallel()
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "occupied", true: "available"}[ready], func(t *testing.T) {
			slots := newTestSlots(t, 1)
			lease := newTaskSlotLease(slots, takeSlot(t, slots), nil)
			lease.release()
			if !ready {
				takeSlot(t, slots)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := lease.acquire(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled acquisition = %v", err)
			}
			lease.release()
			want := 0
			if ready {
				want = 1
			}
			if got := freeSlots(slots); got != want {
				t.Fatalf("free slots after cancellation = %d, want %d", got, want)
			}
		})
	}
}

func TestTaskSlotLeaseAcquireObservesCancellationWhileBlocked(t *testing.T) {
	t.Parallel()
	slots := newTestSlots(t, 1)
	lease := newTaskSlotLease(slots, takeSlot(t, slots), nil)
	lease.release()
	other := takeSlot(t, slots)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- lease.acquire(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("acquisition = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("slot acquisition ignored cancellation")
	}
	lease.release()
	slots.release(other)
	if got := freeSlots(slots); got != 1 {
		t.Fatalf("free slots = %d, want 1", got)
	}
}

func TestTaskSlotLeaseTryAcquireDoesNotWait(t *testing.T) {
	t.Parallel()
	slots := newTestSlots(t, 1)
	lease := newTaskSlotLease(slots, takeSlot(t, slots), nil)
	lease.release()
	other := takeSlot(t, slots)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if acquired, err := lease.tryAcquire(ctx); acquired || err != nil {
		t.Fatalf("occupied slot: acquired=%v error=%v", acquired, err)
	}
	slots.release(other)
	if acquired, err := lease.tryAcquire(ctx); !acquired || err != nil {
		t.Fatalf("available slot: acquired=%v error=%v", acquired, err)
	}
	if acquired, err := lease.tryAcquire(ctx); !acquired || err != nil {
		t.Fatalf("already held slot: acquired=%v error=%v", acquired, err)
	}
	lease.release()
	cancel()
	if acquired, err := lease.tryAcquire(ctx); acquired || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission: acquired=%v error=%v", acquired, err)
	}
	if freeSlots(slots) != 1 {
		t.Fatal("cancelled nonblocking admission consumed capacity")
	}
}
