package kafkaconsumer

import (
	"log"
	"sync"

	"github.com/segmentio/kafka-go"
)

type partitionKey struct {
	topic     string
	partition int
}

// partitionState holds the not-yet-committable messages of one partition in
// fetch order, plus the offsets among them that have finished.
type partitionState struct {
	pending []kafka.Message
	done    map[int64]struct{}
	last    int64 // highest offset tracked so far
}

// offsetTracker decides which offset is safe to commit: a message may only
// be committed once it and every message fetched before it on the same
// partition have finished. Safe for concurrent use.
type offsetTracker struct {
	mu    sync.Mutex
	parts map[partitionKey]*partitionState
}

func newOffsetTracker() *offsetTracker {
	return &offsetTracker{parts: make(map[partitionKey]*partitionState)}
}

// track records a fetched message. Messages of a partition must be tracked
// in fetch (increasing offset) order.
func (t *offsetTracker) track(msg kafka.Message) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := partitionKey{msg.Topic, msg.Partition}
	ps, ok := t.parts[key]
	// An offset at or below the last tracked one means the broker rewound
	// (e.g. a consumer-group generation change). Drop the stale state so the
	// watermark cannot wedge; the full fix is Phase 2.
	if ok && msg.Offset <= ps.last {
		log.Printf("kafkaconsumer: offset rewind on %s[%d]: got %d after %d; resetting partition tracker",
			msg.Topic, msg.Partition, msg.Offset, ps.last)
		ok = false
	}
	if !ok {
		ps = &partitionState{done: make(map[int64]struct{})}
		t.parts[key] = ps
	}
	ps.last = msg.Offset
	ps.pending = append(ps.pending, msg)
}

// done marks msg finished and pops the contiguous finished prefix of its
// partition. It returns the last popped message — the one to commit — and
// true, or false when msg's predecessors are still in flight.
func (t *offsetTracker) done(msg kafka.Message) (kafka.Message, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ps, ok := t.parts[partitionKey{msg.Topic, msg.Partition}]
	if !ok {
		return kafka.Message{}, false
	}
	ps.done[msg.Offset] = struct{}{}

	var last kafka.Message
	popped := false
	for len(ps.pending) > 0 {
		head := ps.pending[0]
		if _, finished := ps.done[head.Offset]; !finished {
			break
		}
		delete(ps.done, head.Offset)
		ps.pending[0] = kafka.Message{} // release payload for GC
		ps.pending = ps.pending[1:]
		last, popped = head, true
	}
	return last, popped
}
