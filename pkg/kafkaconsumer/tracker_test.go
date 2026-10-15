package kafkaconsumer

import (
	"sync"
	"testing"

	"github.com/segmentio/kafka-go"
)

func msgAt(topic string, partition int, offset int64) kafka.Message {
	return kafka.Message{Topic: topic, Partition: partition, Offset: offset}
}

func TestTrackerInOrderCompletionCommitsEach(t *testing.T) {
	tr := newOffsetTracker()
	var e uint64
	for i := int64(0); i < 3; i++ {
		e = tr.track(msgAt("t", 0, i))
	}
	for i := int64(0); i < 3; i++ {
		got, ok := tr.done(msgAt("t", 0, i), e)
		if !ok || got.Offset != i {
			t.Fatalf("done(%d) = (%d, %v), want (%d, true)", i, got.Offset, ok, i)
		}
	}
}

func TestTrackerOutOfOrderCompletionWaitsForPredecessor(t *testing.T) {
	tr := newOffsetTracker()
	tr.track(msgAt("t", 0, 1))
	e := tr.track(msgAt("t", 0, 2))

	if got, ok := tr.done(msgAt("t", 0, 2), e); ok {
		t.Fatalf("done(2) before 1 returned commit for offset %d, want nothing", got.Offset)
	}
	got, ok := tr.done(msgAt("t", 0, 1), e)
	if !ok || got.Offset != 2 {
		t.Fatalf("done(1) = (%d, %v), want (2, true)", got.Offset, ok)
	}
}

func TestTrackerPartitionsAreIndependent(t *testing.T) {
	tr := newOffsetTracker()
	tr.track(msgAt("t", 0, 10)) // stays pending
	e1 := tr.track(msgAt("t", 1, 20))
	eu := tr.track(msgAt("u", 0, 30))

	got, ok := tr.done(msgAt("t", 1, 20), e1)
	if !ok || got.Partition != 1 || got.Offset != 20 {
		t.Fatalf("partition 1 blocked by partition 0: got (%d/%d, %v)", got.Partition, got.Offset, ok)
	}
	got, ok = tr.done(msgAt("u", 0, 30), eu)
	if !ok || got.Topic != "u" || got.Offset != 30 {
		t.Fatalf("topic u blocked by topic t: got (%s/%d, %v)", got.Topic, got.Offset, ok)
	}
}

func TestTrackerConcurrentUse(t *testing.T) {
	tr := newOffsetTracker()
	const n = 200
	var e uint64
	for i := int64(0); i < n; i++ {
		e = tr.track(msgAt("t", 0, i))
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		highest int64 = -1
	)
	for i := int64(0); i < n; i++ {
		wg.Add(1)
		go func(off int64) {
			defer wg.Done()
			if m, ok := tr.done(msgAt("t", 0, off), e); ok {
				mu.Lock()
				if m.Offset > highest {
					highest = m.Offset
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if highest != n-1 {
		t.Fatalf("highest committable offset = %d, want %d", highest, n-1)
	}
}

// partState returns the tracker's internal state for partition 0 of topic t.
func partState(t *testing.T, tr *offsetTracker) *partitionState {
	t.Helper()
	ps, ok := tr.parts[partitionKey{"t", 0}]
	if !ok {
		t.Fatal("partition t/0 not tracked")
	}
	return ps
}

func TestTrackerRewindIgnoresStaleCompletions(t *testing.T) {
	tr := newOffsetTracker()
	var e0 uint64
	for i := int64(0); i < 3; i++ {
		e0 = tr.track(msgAt("t", 0, i))
	}
	// Generation change: the broker re-delivers from committed offset 1.
	e1 := tr.track(msgAt("t", 0, 1))
	if e1 == e0 {
		t.Fatalf("rewind kept epoch %d, want a new one", e0)
	}

	for _, off := range []int64{1, 2} {
		if got, ok := tr.done(msgAt("t", 0, off), e0); ok {
			t.Fatalf("stale done(%d, e0) committed offset %d, want nothing", off, got.Offset)
		}
		if ps := partState(t, tr); len(ps.done) != 0 {
			t.Fatalf("stale done(%d, e0) left done entries %v", off, ps.done)
		}
	}

	got, ok := tr.done(msgAt("t", 0, 1), e1)
	if !ok || got.Offset != 1 {
		t.Fatalf("done(1', e1) = (%d, %v), want (1, true)", got.Offset, ok)
	}
	if e := tr.track(msgAt("t", 0, 2)); e != e1 {
		t.Fatalf("track(2') epoch = %d, want %d (no rewind)", e, e1)
	}
	got, ok = tr.done(msgAt("t", 0, 2), e1)
	if !ok || got.Offset != 2 {
		t.Fatalf("done(2', e1) = (%d, %v), want (2, true)", got.Offset, ok)
	}

	ps := partState(t, tr)
	if len(ps.done) != 0 || len(ps.pending) != 0 {
		t.Fatalf("leftover state: pending %v, done %v", offsets(ps.pending), ps.done)
	}
}

func TestTrackerDoneUnknownOffsetIsNoOp(t *testing.T) {
	tr := newOffsetTracker()
	tr.track(msgAt("t", 0, 5))
	e := tr.track(msgAt("t", 0, 7))

	for _, off := range []int64{3, 6, 9} {
		if got, ok := tr.done(msgAt("t", 0, off), e); ok {
			t.Fatalf("done(%d) of untracked offset committed %d, want nothing", off, got.Offset)
		}
	}
	ps := partState(t, tr)
	if len(ps.done) != 0 {
		t.Fatalf("untracked done created entries %v", ps.done)
	}
	if got := offsets(ps.pending); len(got) != 2 || got[0] != 5 || got[1] != 7 {
		t.Fatalf("pending = %v, want [5 7]", got)
	}
	if got, ok := tr.done(msgAt("u", 0, 0), 0); ok {
		t.Fatalf("done on unknown partition committed %d, want nothing", got.Offset)
	}
}
