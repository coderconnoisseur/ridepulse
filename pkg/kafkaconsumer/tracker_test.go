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
	for i := int64(0); i < 3; i++ {
		tr.track(msgAt("t", 0, i))
	}
	for i := int64(0); i < 3; i++ {
		got, ok := tr.done(msgAt("t", 0, i))
		if !ok || got.Offset != i {
			t.Fatalf("done(%d) = (%d, %v), want (%d, true)", i, got.Offset, ok, i)
		}
	}
}

func TestTrackerOutOfOrderCompletionWaitsForPredecessor(t *testing.T) {
	tr := newOffsetTracker()
	tr.track(msgAt("t", 0, 1))
	tr.track(msgAt("t", 0, 2))

	if got, ok := tr.done(msgAt("t", 0, 2)); ok {
		t.Fatalf("done(2) before 1 returned commit for offset %d, want nothing", got.Offset)
	}
	got, ok := tr.done(msgAt("t", 0, 1))
	if !ok || got.Offset != 2 {
		t.Fatalf("done(1) = (%d, %v), want (2, true)", got.Offset, ok)
	}
}

func TestTrackerPartitionsAreIndependent(t *testing.T) {
	tr := newOffsetTracker()
	tr.track(msgAt("t", 0, 10)) // stays pending
	tr.track(msgAt("t", 1, 20))
	tr.track(msgAt("u", 0, 30))

	got, ok := tr.done(msgAt("t", 1, 20))
	if !ok || got.Partition != 1 || got.Offset != 20 {
		t.Fatalf("partition 1 blocked by partition 0: got (%d/%d, %v)", got.Partition, got.Offset, ok)
	}
	got, ok = tr.done(msgAt("u", 0, 30))
	if !ok || got.Topic != "u" || got.Offset != 30 {
		t.Fatalf("topic u blocked by topic t: got (%s/%d, %v)", got.Topic, got.Offset, ok)
	}
}

func TestTrackerConcurrentUse(t *testing.T) {
	tr := newOffsetTracker()
	const n = 200
	for i := int64(0); i < n; i++ {
		tr.track(msgAt("t", 0, i))
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
			if m, ok := tr.done(msgAt("t", 0, off)); ok {
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
