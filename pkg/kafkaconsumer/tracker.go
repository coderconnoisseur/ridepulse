package kafkaconsumer

import (
	"cmp"
	"log"
	"slices"
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
	// epoch advances on every rewind; completions carrying an older epoch
	// belong to copies that were superseded by a re-fetch.
	epoch uint64
	last  int64 // highest offset tracked in the current epoch, -1 if none
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

// track records a fetched message and returns the epoch to pass to done.
// An offset at or below the last tracked one means the reader rewound (e.g.
// after a consumer-group rebalance it re-fetches from the committed offset):
// the partition starts a new epoch and its old pending state is discarded.
func (t *offsetTracker) track(msg kafka.Message) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := partitionKey{msg.Topic, msg.Partition}
	ps, ok := t.parts[key]
	if !ok {
		ps = &partitionState{done: make(map[int64]struct{}), last: -1}
		t.parts[key] = ps
	}
	if msg.Offset <= ps.last {
		log.Printf("kafkaconsumer: %s/%d rewound from %d to %d; ignoring in-flight completions of the old fetch",
			msg.Topic, msg.Partition, ps.last, msg.Offset)
		ps.epoch++
		clear(ps.pending) // release payloads for GC
		ps.pending = ps.pending[:0]
		clear(ps.done)
	}
	ps.pending = append(ps.pending, msg)
	ps.last = msg.Offset
	return ps.epoch
}

// done marks msg finished and pops the contiguous finished prefix of its
// partition. It returns the last popped message — the one to commit — and
// true, or false when msg's predecessors are still in flight. A completion
// from an older epoch, or for an offset not pending, changes nothing: after
// a rewind the re-fetched copy is processed again and commits instead.
func (t *offsetTracker) done(msg kafka.Message, epoch uint64) (kafka.Message, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ps, ok := t.parts[partitionKey{msg.Topic, msg.Partition}]
	if !ok || epoch != ps.epoch {
		return kafka.Message{}, false
	}
	// pending is in increasing offset order within an epoch.
	if _, found := slices.BinarySearchFunc(ps.pending, msg.Offset, func(m kafka.Message, off int64) int {
		return cmp.Compare(m.Offset, off)
	}); !found {
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
