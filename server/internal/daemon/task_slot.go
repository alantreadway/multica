package daemon

import "context"

// taskSlotLease belongs to one task goroutine. A parked task keeps its task
// ownership but returns execution capacity, then may resume on a different index.
type taskSlotLease struct {
	slots  *agTaskSlots // AG patch: machine-wide slot locks (see ag_slotlock.go)
	wakeup chan struct{}
	index  int
	held   bool
}

func newTaskSlotLease(slots *agTaskSlots, index int, wakeup chan struct{}) *taskSlotLease {
	return &taskSlotLease{slots: slots, wakeup: wakeup, index: index, held: true}
}

func (s *taskSlotLease) release() {
	if !s.held {
		return
	}
	s.held = false
	s.slots.release(s.index) // AG patch: drop the slot flock
	signalPollerWakeup(s.wakeup)
}

func (s *taskSlotLease) acquire(ctx context.Context) error {
	if acquired, err := s.tryAcquire(ctx); acquired || err != nil {
		return err
	}
	index, err := s.slots.waitTake(ctx) // AG patch: poll the slot flocks
	if err != nil {
		return err
	}
	return s.adopt(ctx, index)
}

func (s *taskSlotLease) tryAcquire(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.held {
		return true, nil
	}
	index, ok := s.slots.takeOne() // AG patch
	if !ok {
		return false, nil
	}
	err := s.adopt(ctx, index)
	return err == nil, err
}

func (s *taskSlotLease) adopt(ctx context.Context, index int) error {
	s.index = index
	s.held = true
	// A ready token can win a select at the same time as cancellation.
	if err := ctx.Err(); err != nil {
		s.release()
		return err
	}
	return nil
}
