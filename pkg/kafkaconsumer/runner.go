// Package kafkaconsumer runs a Kafka consumer with at-least-once delivery:
// an offset is committed only after its message, and every earlier message
// of the same partition, has been handled, dead-lettered or dropped.
package kafkaconsumer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/segmentio/kafka-go"
)

// Reader is the subset of *kafka.Reader the runner needs.
type Reader interface {
	FetchMessage(ctx context.Context) (kafka.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafka.Message) error
}

// Writer is the subset of *kafka.Writer the runner needs for the DLQ. The
// runner sets Topic on every DLQ message, so a *kafka.Writer used here must
// not have its own Topic configured.
type Writer interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

// Handler processes one message. Returning nil marks it done; an error
// wrapped with Permanent skips retries; any other error is retried.
type Handler func(ctx context.Context, msg kafka.Message) error

const (
	defaultMaxRetries   = 3
	defaultRetryBackoff = 50 * time.Millisecond
	maxRetryBackoff     = time.Second
	defaultDrainTimeout = 10 * time.Second
	commitTimeout       = 5 * time.Second
)

// Config configures Run.
type Config struct {
	Workers      int           // required, > 0
	MaxRetries   int           // retries after first attempt; 0 → default 3; <0 → no retries
	RetryBackoff time.Duration // base for exponential backoff; 0 → 50ms; capped at 1s
	DLQ          Writer        // nil → log + drop after retries (still committed)
	DLQTopic     string        // required when DLQ != nil
	DrainTimeout time.Duration // 0 → 10s
	// OnRetry is called before each retry; attempt is the 1-based number of
	// the attempt that just failed.
	OnRetry func(msg kafka.Message, attempt int, err error)
	// OnDLQ is called once a message is dead-lettered: written to the DLQ,
	// or dropped when DLQ is nil.
	OnDLQ func(msg kafka.Message, err error)
}

// runner holds the state shared by the fetch loop and the workers of one Run.
type runner struct {
	r       Reader
	cfg     Config
	h       Handler
	tracker *offsetTracker

	// hctx is passed to handlers, backoff sleeps and DLQ writes. It survives
	// shutdown and is cancelled only when the drain timeout expires.
	hctx context.Context

	commitMu sync.Mutex
	stopped  bool // set under commitMu once Run gives up on in-flight work

	fatalMu     sync.Mutex
	fatalErr    error
	stopFetchFn context.CancelFunc
}

// Run blocks until ctx is cancelled (returns nil after a clean drain) or
// a fatal error occurs (fetch error not caused by ctx, commit error, or a
// DLQ write that failed after retries) — then it drains and returns it.
// Run never closes the Reader or DLQ Writer; the caller owns them.
func Run(ctx context.Context, r Reader, cfg Config, h Handler) error {
	if err := cfg.validate(r, h); err != nil {
		return err
	}
	cfg.applyDefaults()

	hctx, cancelHandlers := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelHandlers()
	fetchCtx, stopFetch := context.WithCancel(ctx)
	defer stopFetch()

	rn := &runner{
		r: r, cfg: cfg, h: h,
		tracker:     newOffsetTracker(),
		hctx:        hctx,
		stopFetchFn: stopFetch,
	}

	work := make(chan kafka.Message) // unbuffered: backpressure on fetching
	var wg sync.WaitGroup
	for range cfg.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for msg := range work {
				rn.process(msg)
			}
		}()
	}

	rn.fetchLoop(fetchCtx, work)
	close(work)

	drained := make(chan struct{})
	go func() {
		wg.Wait()
		close(drained)
	}()
	timer := time.NewTimer(cfg.DrainTimeout)
	defer timer.Stop()
	select {
	case <-drained:
	case <-timer.C:
		log.Printf("kafkaconsumer: drain timeout %s expired; leaving in-flight messages uncommitted", cfg.DrainTimeout)
		rn.commitMu.Lock()
		rn.stopped = true
		rn.commitMu.Unlock()
		cancelHandlers()
	}

	// Every terminal message was committed as it completed, so there is no
	// pending commit left to flush here.
	return rn.err()
}

func (c Config) validate(r Reader, h Handler) error {
	switch {
	case r == nil:
		return errors.New("kafkaconsumer: nil Reader")
	case h == nil:
		return errors.New("kafkaconsumer: nil Handler")
	case c.Workers <= 0:
		return fmt.Errorf("kafkaconsumer: Workers must be > 0, got %d", c.Workers)
	case c.DLQ != nil && c.DLQTopic == "":
		return errors.New("kafkaconsumer: DLQTopic is required when DLQ is set")
	}
	return nil
}

func (c *Config) applyDefaults() {
	switch {
	case c.MaxRetries == 0:
		c.MaxRetries = defaultMaxRetries
	case c.MaxRetries < 0:
		c.MaxRetries = 0
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = defaultRetryBackoff
	}
	if c.DrainTimeout <= 0 {
		c.DrainTimeout = defaultDrainTimeout
	}
}

func (rn *runner) fetchLoop(ctx context.Context, work chan<- kafka.Message) {
	for {
		msg, err := rn.r.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() == nil {
				rn.fatal(fmt.Errorf("kafkaconsumer: fetch: %w", err))
			}
			return
		}
		rn.tracker.track(msg)
		select {
		case work <- msg:
		case <-ctx.Done():
			// Tracked but never handed off: it stays pending, which blocks
			// commits past it, so it is redelivered on restart.
			return
		}
	}
}

// fatal records the first fatal error and stops fetching; Run then drains
// and returns it.
func (rn *runner) fatal(err error) {
	log.Printf("%v", err)
	rn.fatalMu.Lock()
	defer rn.fatalMu.Unlock()
	if rn.fatalErr == nil {
		rn.fatalErr = err
		rn.stopFetchFn()
	}
}

func (rn *runner) err() error {
	rn.fatalMu.Lock()
	defer rn.fatalMu.Unlock()
	return rn.fatalErr
}

// process drives one message to a terminal state and commits what that
// unblocks. If the message cannot reach a terminal state (drain timeout, or
// the DLQ write failed) it is left uncommitted.
func (rn *runner) process(msg kafka.Message) {
	err := rn.handleWithRetry(msg)
	if err != nil {
		if rn.hctx.Err() != nil {
			return // drain timeout hit mid-flight
		}
		if !rn.deadLetter(msg, err) {
			return
		}
	}
	rn.complete(msg)
}

func (rn *runner) handleWithRetry(msg kafka.Message) error {
	for attempt := 1; ; attempt++ {
		err := rn.h(rn.hctx, msg)
		if err == nil || IsPermanent(err) || attempt > rn.cfg.MaxRetries || rn.hctx.Err() != nil {
			return err
		}
		if rn.cfg.OnRetry != nil {
			rn.cfg.OnRetry(msg, attempt, err)
		}
		if !rn.sleep(attempt) {
			return err
		}
	}
}

// deadLetter routes a failed message to the DLQ (or drops it when no DLQ is
// configured). It reports whether the message reached a terminal state.
func (rn *runner) deadLetter(msg kafka.Message, cause error) bool {
	if rn.cfg.DLQ == nil {
		log.Printf("kafkaconsumer: dropping %s/%d@%d after failure: %v", msg.Topic, msg.Partition, msg.Offset, cause)
		if rn.cfg.OnDLQ != nil {
			rn.cfg.OnDLQ(msg, cause)
		}
		return true
	}

	dlqMsg := kafka.Message{
		Topic: rn.cfg.DLQTopic,
		Key:   msg.Key,
		Value: msg.Value,
		Headers: append(append(make([]kafka.Header, 0, len(msg.Headers)+4), msg.Headers...),
			kafka.Header{Key: "x-error", Value: []byte(cause.Error())},
			kafka.Header{Key: "x-original-topic", Value: []byte(msg.Topic)},
			kafka.Header{Key: "x-original-partition", Value: []byte(strconv.Itoa(msg.Partition))},
			kafka.Header{Key: "x-original-offset", Value: []byte(strconv.FormatInt(msg.Offset, 10))},
		),
	}
	for attempt := 1; ; attempt++ {
		err := rn.cfg.DLQ.WriteMessages(rn.hctx, dlqMsg)
		if err == nil {
			if rn.cfg.OnDLQ != nil {
				rn.cfg.OnDLQ(msg, cause)
			}
			return true
		}
		if rn.hctx.Err() != nil {
			return false
		}
		if attempt > rn.cfg.MaxRetries {
			rn.fatal(fmt.Errorf("kafkaconsumer: DLQ write for %s/%d@%d: %w", msg.Topic, msg.Partition, msg.Offset, err))
			return false
		}
		if !rn.sleep(attempt) {
			return false // drain timeout: leave uncommitted, not a DLQ failure
		}
	}
}

// sleep waits the backoff for the given failed attempt. It returns false if
// the drain timeout expired first.
func (rn *runner) sleep(attempt int) bool {
	d := rn.cfg.RetryBackoff
	for i := 1; i < attempt && d < maxRetryBackoff; i++ {
		d *= 2
	}
	d = min(d, maxRetryBackoff)

	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-rn.hctx.Done():
		return false
	}
}

// complete marks msg terminal and commits the highest offset that became
// committable. Holding commitMu across done+commit keeps commits serialized
// and per-partition monotonic: a later done() can only pop higher offsets.
func (rn *runner) complete(msg kafka.Message) {
	rn.commitMu.Lock()
	defer rn.commitMu.Unlock()
	if rn.stopped {
		return
	}
	toCommit, ok := rn.tracker.done(msg)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), commitTimeout)
	defer cancel()
	if err := rn.r.CommitMessages(ctx, toCommit); err != nil {
		rn.fatal(fmt.Errorf("kafkaconsumer: commit %s/%d@%d: %w", toCommit.Topic, toCommit.Partition, toCommit.Offset, err))
	}
}
