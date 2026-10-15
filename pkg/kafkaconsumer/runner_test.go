package kafkaconsumer

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

// fakeReader serves a fixed list of messages, then blocks until ctx is done.
type fakeReader struct {
	msgs chan kafka.Message

	mu      sync.Mutex
	commits []kafka.Message
	notify  chan struct{} // receives (non-blocking) after every commit
}

func newFakeReader(msgs ...kafka.Message) *fakeReader {
	ch := make(chan kafka.Message, len(msgs))
	for _, m := range msgs {
		ch <- m
	}
	return &fakeReader{msgs: ch, notify: make(chan struct{}, 1)}
}

func (f *fakeReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	select {
	case m := <-f.msgs:
		return m, nil
	case <-ctx.Done():
		return kafka.Message{}, ctx.Err()
	}
}

func (f *fakeReader) CommitMessages(_ context.Context, msgs ...kafka.Message) error {
	f.mu.Lock()
	f.commits = append(f.commits, msgs...)
	f.mu.Unlock()
	select {
	case f.notify <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeReader) committed() []kafka.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]kafka.Message(nil), f.commits...)
}

// waitCommitted blocks until some commit for partition 0 reaches offset.
func (f *fakeReader) waitCommitted(t *testing.T, offset int64) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		for _, m := range f.committed() {
			if m.Offset >= offset {
				return
			}
		}
		select {
		case <-f.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for commit of offset %d; commits: %v", offset, offsets(f.committed()))
		}
	}
}

type fakeWriter struct {
	mu   sync.Mutex
	msgs []kafka.Message
	err  error // returned from every write when non-nil
}

func (w *fakeWriter) WriteMessages(_ context.Context, msgs ...kafka.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.msgs = append(w.msgs, msgs...)
	return nil
}

func (w *fakeWriter) written() []kafka.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]kafka.Message(nil), w.msgs...)
}

func offsets(msgs []kafka.Message) []int64 {
	out := make([]int64, len(msgs))
	for i, m := range msgs {
		out[i] = m.Offset
	}
	return out
}

func seq(topic string, n int) []kafka.Message {
	out := make([]kafka.Message, n)
	for i := range out {
		out[i] = kafka.Message{
			Topic:     topic,
			Partition: 0,
			Offset:    int64(i),
			Key:       []byte("k" + strconv.Itoa(i)),
			Value:     []byte("v" + strconv.Itoa(i)),
		}
	}
	return out
}

// startRun runs Run in the background and returns a cancel func plus a
// channel carrying Run's result.
func startRun(r Reader, cfg Config, h Handler) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, r, cfg, h) }()
	return cancel, errc
}

func waitRun(t *testing.T, errc <-chan error) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func assertMonotonic(t *testing.T, commits []kafka.Message) {
	t.Helper()
	last := map[int]int64{}
	for _, m := range commits {
		if prev, ok := last[m.Partition]; ok && m.Offset < prev {
			t.Fatalf("commit regressed on partition %d: %d after %d (all: %v)", m.Partition, m.Offset, prev, offsets(commits))
		}
		last[m.Partition] = m.Offset
	}
}

func TestRunSuccessCommitsEveryOffsetHighestLast(t *testing.T) {
	const n = 50
	r := newFakeReader(seq("t", n)...)
	cancel, errc := startRun(r, Config{Workers: 4, RetryBackoff: time.Millisecond},
		func(context.Context, kafka.Message) error { return nil })

	r.waitCommitted(t, n-1)
	cancel()
	if err := waitRun(t, errc); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}

	commits := r.committed()
	assertMonotonic(t, commits)
	if got := commits[len(commits)-1].Offset; got != n-1 {
		t.Fatalf("last commit = %d, want %d", got, n-1)
	}
}

func TestRunSlowHeadBlocksCommitsUntilItFinishes(t *testing.T) {
	r := newFakeReader(seq("t", 5)...)
	release := make(chan struct{})
	var fastDone sync.WaitGroup
	fastDone.Add(4)

	cancel, errc := startRun(r, Config{Workers: 5, RetryBackoff: time.Millisecond},
		func(_ context.Context, m kafka.Message) error {
			if m.Offset == 0 {
				<-release
				return nil
			}
			fastDone.Done()
			return nil
		})
	defer cancel()

	fastDone.Wait()
	// Give any (incorrect) commit from the fast workers time to land.
	time.Sleep(50 * time.Millisecond)
	if c := r.committed(); len(c) != 0 {
		t.Fatalf("committed %v while offset 0 still processing, want nothing", offsets(c))
	}

	close(release)
	r.waitCommitted(t, 4)
	cancel()
	if err := waitRun(t, errc); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	if c := r.committed(); len(c) != 1 || c[0].Offset != 4 {
		t.Fatalf("commits = %v, want exactly [4]", offsets(c))
	}
}

func TestRunTransientErrorRetriesThenCommits(t *testing.T) {
	r := newFakeReader(seq("t", 1)...)
	dlq := &fakeWriter{}
	var calls atomic.Int32
	cancel, errc := startRun(r, Config{Workers: 1, RetryBackoff: time.Millisecond, DLQ: dlq, DLQTopic: "t.dlq"},
		func(context.Context, kafka.Message) error {
			if calls.Add(1) <= 2 {
				return errors.New("transient")
			}
			return nil
		})

	r.waitCommitted(t, 0)
	cancel()
	if err := waitRun(t, errc); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("handler called %d times, want 3", got)
	}
	if w := dlq.written(); len(w) != 0 {
		t.Fatalf("DLQ received %d messages, want 0", len(w))
	}
}

func TestRunExhaustedRetriesGoToDLQThenCommit(t *testing.T) {
	src := kafka.Message{
		Topic: "t", Partition: 3, Offset: 7,
		Key: []byte("ride-1"), Value: []byte(`{"x":1}`),
		Headers: []kafka.Header{{Key: "trace", Value: []byte("abc")}},
	}
	r := newFakeReader(src)
	dlq := &fakeWriter{}
	var calls, retries, dlqHook atomic.Int32
	cancel, errc := startRun(r, Config{
		Workers: 1, MaxRetries: 2, RetryBackoff: time.Millisecond,
		DLQ: dlq, DLQTopic: "t.dlq",
		OnRetry: func(kafka.Message, int, error) { retries.Add(1) },
		OnDLQ:   func(kafka.Message, error) { dlqHook.Add(1) },
	}, func(context.Context, kafka.Message) error {
		calls.Add(1)
		return errors.New("boom")
	})

	r.waitCommitted(t, 7)
	cancel()
	if err := waitRun(t, errc); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("handler called %d times, want 3", got)
	}
	if got := retries.Load(); got != 2 {
		t.Fatalf("OnRetry called %d times, want 2", got)
	}
	if got := dlqHook.Load(); got != 1 {
		t.Fatalf("OnDLQ called %d times, want 1", got)
	}

	w := dlq.written()
	if len(w) != 1 {
		t.Fatalf("DLQ received %d messages, want 1", len(w))
	}
	d := w[0]
	if d.Topic != "t.dlq" || string(d.Key) != "ride-1" || string(d.Value) != `{"x":1}` {
		t.Fatalf("DLQ message = topic %q key %q value %q", d.Topic, d.Key, d.Value)
	}
	want := map[string]string{
		"trace":                "abc",
		"x-error":              "boom",
		"x-original-topic":     "t",
		"x-original-partition": "3",
		"x-original-offset":    "7",
	}
	got := map[string]string{}
	for _, h := range d.Headers {
		got[h.Key] = string(h.Value)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("header %q = %q, want %q", k, got[k], v)
		}
	}
	if len(src.Headers) != 1 {
		t.Errorf("original message headers mutated: %v", src.Headers)
	}
}

func TestRunPermanentErrorSkipsRetries(t *testing.T) {
	r := newFakeReader(seq("t", 1)...)
	dlq := &fakeWriter{}
	var calls atomic.Int32
	cancel, errc := startRun(r, Config{Workers: 1, MaxRetries: 5, RetryBackoff: time.Millisecond, DLQ: dlq, DLQTopic: "t.dlq"},
		func(context.Context, kafka.Message) error {
			calls.Add(1)
			return Permanent(errors.New("bad payload"))
		})

	r.waitCommitted(t, 0)
	cancel()
	if err := waitRun(t, errc); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler called %d times, want 1", got)
	}
	w := dlq.written()
	if len(w) != 1 {
		t.Fatalf("DLQ received %d messages, want 1", len(w))
	}
	for _, h := range w[0].Headers {
		if h.Key == "x-error" && string(h.Value) != "bad payload" {
			t.Fatalf("x-error = %q, want %q", h.Value, "bad payload")
		}
	}
}

func TestRunNilDLQDropsAndCommits(t *testing.T) {
	r := newFakeReader(seq("t", 1)...)
	var dlqHook atomic.Int32
	cancel, errc := startRun(r, Config{Workers: 1, MaxRetries: -1, OnDLQ: func(kafka.Message, error) { dlqHook.Add(1) }},
		func(context.Context, kafka.Message) error { return errors.New("boom") })

	r.waitCommitted(t, 0)
	cancel()
	if err := waitRun(t, errc); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	if got := dlqHook.Load(); got != 1 {
		t.Fatalf("OnDLQ called %d times, want 1", got)
	}
}

func TestRunFailingDLQIsFatalAndLeavesOffsetUncommitted(t *testing.T) {
	r := newFakeReader(seq("t", 2)...)
	dlqErr := errors.New("dlq down")
	dlq := &fakeWriter{err: dlqErr}
	_, errc := startRun(r, Config{Workers: 2, MaxRetries: 1, RetryBackoff: time.Millisecond, DLQ: dlq, DLQTopic: "t.dlq"},
		func(_ context.Context, m kafka.Message) error {
			if m.Offset == 0 {
				return errors.New("boom")
			}
			return nil
		})

	err := waitRun(t, errc)
	if !errors.Is(err, dlqErr) {
		t.Fatalf("Run returned %v, want error wrapping %v", err, dlqErr)
	}
	// Offset 1 succeeded, but committing it would also commit 0.
	if c := r.committed(); len(c) != 0 {
		t.Fatalf("commits = %v, want none", offsets(c))
	}
}

func TestRunCancelWhileHandlerInFlightDrainsAndCommits(t *testing.T) {
	r := newFakeReader(seq("t", 1)...)
	started := make(chan struct{})
	release := make(chan struct{})
	var handlerCtxCancelled atomic.Bool

	cancel, errc := startRun(r, Config{Workers: 1, RetryBackoff: time.Millisecond, DrainTimeout: 5 * time.Second},
		func(ctx context.Context, _ kafka.Message) error {
			close(started)
			<-release
			handlerCtxCancelled.Store(ctx.Err() != nil)
			return nil
		})

	<-started
	cancel()
	select {
	case err := <-errc:
		t.Fatalf("Run returned %v before in-flight handler finished", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)

	if err := waitRun(t, errc); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	if handlerCtxCancelled.Load() {
		t.Fatal("handler ctx was cancelled by shutdown")
	}
	if c := r.committed(); len(c) != 1 || c[0].Offset != 0 {
		t.Fatalf("commits = %v, want [0]", offsets(c))
	}
}

func TestRunDrainTimeoutCancelsHandlerAndSkipsCommit(t *testing.T) {
	r := newFakeReader(seq("t", 1)...)
	started := make(chan struct{})
	cancel, errc := startRun(r, Config{Workers: 1, RetryBackoff: time.Millisecond, DrainTimeout: 20 * time.Millisecond},
		func(ctx context.Context, _ kafka.Message) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})

	<-started
	cancel()
	if err := waitRun(t, errc); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	if c := r.committed(); len(c) != 0 {
		t.Fatalf("commits = %v, want none", offsets(c))
	}
}

func TestRunRejectsInvalidConfig(t *testing.T) {
	r := newFakeReader()
	h := func(context.Context, kafka.Message) error { return nil }
	cases := map[string]Config{
		"zero workers":      {Workers: 0},
		"dlq without topic": {Workers: 1, DLQ: &fakeWriter{}},
	}
	for name, cfg := range cases {
		if err := Run(context.Background(), r, cfg, h); err == nil {
			t.Errorf("%s: Run returned nil, want config error", name)
		}
	}
}

func TestPermanentIsDetectableThroughWrapping(t *testing.T) {
	base := errors.New("bad")
	err := Permanent(base)
	if !IsPermanent(err) || !IsPermanent(errors.Join(errors.New("ctx"), err)) {
		t.Fatal("IsPermanent did not detect wrapped permanent error")
	}
	if !errors.Is(err, base) || err.Error() != "bad" {
		t.Fatalf("Permanent changed error identity/text: %v", err)
	}
	if IsPermanent(base) || IsPermanent(nil) || Permanent(nil) != nil {
		t.Fatal("non-permanent errors misclassified")
	}
}
