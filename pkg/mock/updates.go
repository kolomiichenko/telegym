package mock

import (
	"context"
	"sync"
	"time"
)

// updateQueue retains the latest 5000 unconfirmed updates per bot.
// Closing notify wakes a waiting poll when an update arrives.
type updateQueue struct {
	pollMu  sync.Mutex
	mu      sync.Mutex
	updates []Update
	notify  chan struct{}
}

const maxPendingUpdates = 5000

func newUpdateQueue() *updateQueue {
	return &updateQueue{notify: make(chan struct{})}
}

func (q *updateQueue) Enqueue(upd Update) {
	q.mu.Lock()
	q.updates = append(q.updates, upd)
	if over := len(q.updates) - maxPendingUpdates; over > 0 {
		copy(q.updates, q.updates[over:])
		q.updates = q.updates[:maxPendingUpdates]
	}
	close(q.notify)
	q.notify = make(chan struct{})
	q.mu.Unlock()
}

func (q *updateQueue) Poll(ctx context.Context, offset int64, limit int, timeout time.Duration) []Update {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var timer *time.Timer
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		defer timer.Stop()
	}

	for {
		q.mu.Lock()
		q.applyOffset(offset)
		if len(q.updates) > 0 {
			n := min(limit, len(q.updates))
			out := append([]Update(nil), q.updates[:n]...)
			q.mu.Unlock()
			return out
		}
		notify := q.notify
		q.mu.Unlock()

		if timeout <= 0 {
			return []Update{}
		}
		select {
		case <-ctx.Done():
			return []Update{}
		case <-timer.C:
			return []Update{}
		case <-notify:
		}
	}
}

func (q *updateQueue) applyOffset(offset int64) {
	switch {
	case offset > 0:
		first := 0
		for first < len(q.updates) && q.updates[first].UpdateID < offset {
			first++
		}
		clear(q.updates[:first])
		q.updates = q.updates[first:]
	case offset < 0:
		if offset > -int64(len(q.updates)) {
			keep := int(-offset)
			clear(q.updates[:len(q.updates)-keep])
			q.updates = q.updates[len(q.updates)-keep:]
		}
	}
}

func (q *updateQueue) Clear() {
	q.mu.Lock()
	q.updates = nil
	q.mu.Unlock()
}

func (q *updateQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.updates)
}
