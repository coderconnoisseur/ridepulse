# Deadline Fix — Matching Service

> **Status:** local-only note. Not intended for the public repo.
> Documents a deadline-hierarchy bug in `services/matching-service` that
> silently collapsed progressive batching into single-batch matching and
> occasionally starved Kafka publishes, plus the fix applied.

---

## 1. The bug in one paragraph

The matching service had three time budgets nominally in play:

1. **Job budget** — how long the worker has for a single ride, end to end.
2. **Per-batch budget** — how long we wait for a batch of parallel `SETNX`
   attempts to produce a winner before moving to the next batch.
3. **Publish budget** — how long we allow the Kafka `WriteMessages` call
   after a match succeeds.

In the pre-fix code, all three shared a single **300 ms** context, and the
per-batch budget was **400 ms**. Because parent-context cancellation
propagates to every child, the 300 ms job budget was the binding constraint
on *everything* — the 400 ms per-batch timer could never fire, and any
match that ate most of the 300 ms left the Kafka publish with a scrap of
time (sometimes zero). What the README calls "progressive parallel
locking" was in practice **one batch attempt per ride**, and successful
matches sometimes silently failed to publish `ride.matched`.

---

## 2. What the pre-fix code did

### 2a. The single shared context

`services/matching-service/internal/app/app.go` (pre-fix):

```go
pool.Start(ctx, func(event domain.RidePricedEvent) error {
    jobctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
    defer cancel()

    match, err := matcher.Match(jobctx, event)
    if err != nil {
        log.Printf("matching failed for ride %s: %v", event.RideID, err)
        return err
    }
    return publisher.PublishRideMatched(jobctx, match)  // <-- same jobctx
})
```

One 300 ms budget covers both `matcher.Match` and `PublishRideMatched`.

### 2b. The per-batch deadline that never actually fired

`services/matching-service/internal/matching/matching.go` (pre-fix):

```go
select {
case driverID := <-resultCh:
    // winner
case <-time.After(400 * time.Millisecond):   // <-- 400 ms per batch
    cancel()
}
```

But the entire matching loop is wrapped in `jobctx` (300 ms). Once 300 ms
of wall-clock elapses, `jobctx.Done()` fires, `TryLockDriver` returns with
context error, and the batch loop's next-iteration guard
(`case <-ctx.Done(): return ctx.Err()`) exits. The 400 ms `time.After`
literally never gets a chance to run to completion — it is always dominated
by the outer 300 ms.

---

## 3. Why it matters in practice

### 3a. Progressive batching wasn't progressive

The Redis `GEORADIUS` query returns up to 20 drivers (`Count:20` in
`driver_repository.go`). Batches are size 5. So the loop *could* execute
up to 4 batches per ride.

Sequential runtime = (batches completed) × (per-batch wall-clock).

With 300 ms outer / 400 ms per-batch:

| Scenario                             | Batches actually attempted |
|--------------------------------------|----------------------------|
| First batch wins in 50 ms            | 1 (fine)                   |
| First batch wins in 250 ms           | 1 (fine — happy path)      |
| First batch has no winner            | 1 (outer deadline hit before 400 ms tick) |

The "no winner in first batch" case is exactly where progressive batching
was supposed to help — try the next 5 drivers. Instead, the ride simply
fails with "no drivers available." Under a hotspot workload (which is
what the rider simulator produces) this is common: the top-5 nearest
drivers are all lock-contended, batches 2 through 4 would find free
drivers a bit further out, and the match would succeed — except we never
get to run those batches.

### 3b. Silent Kafka publish failures

When a match takes 250 ms, `jobctx` has 50 ms left. `WriteMessages` with
`RequiredAcks: kafka.RequireOne` needs a full producer→broker→ack round
trip. On a stressed local Kafka, that RTT can exceed 50 ms. Result: the
match succeeds in Redis (a driver *is* locked and paid for), but
`ride.matched` is never emitted. Because the phantom-lock guard now
releases *unclaimed* successful locks, the specific winner in this failure
case is still locked to a rideID whose event never landed — a lock leak
of a different flavour, invisible without tracking publish errors
separately from match failures.

There is currently no metric that distinguishes "match failed at Redis"
from "match succeeded but publish timed out" — a match-succeeded-then-
publish-failed event appears in the pool as `_ = handler(event)` with the
error swallowed. So this failure mode was completely silent.

---

## 4. The fix

### 4a. Two independent context budgets

`services/matching-service/internal/app/app.go`:

```go
const (
    matchDeadline   = 800 * time.Millisecond
    publishDeadline = 300 * time.Millisecond
)

pool.Start(ctx, func(event domain.RidePricedEvent) error {
    matchCtx, matchCancel := context.WithTimeout(ctx, matchDeadline)
    defer matchCancel()

    match, err := matcher.Match(matchCtx, event)
    if err != nil {
        log.Printf("matching failed for ride %s: %v", event.RideID, err)
        return err
    }

    publishCtx, publishCancel := context.WithTimeout(ctx, publishDeadline)
    defer publishCancel()
    return publisher.PublishRideMatched(publishCtx, match)
})
```

Key point: `publishCtx` is derived from `ctx` (the pool's ctx), not from
`matchCtx`. It starts fresh at 300 ms whether the match ran for 50 ms or
750 ms.

### 4b. Per-batch deadline reduced to fit inside the match budget

`services/matching-service/internal/matching/matching.go`:

```go
const BatchDeadline = 200 * time.Millisecond

const (
    batchSize         = 5
    batchDeadline     = BatchDeadline
    releaseTimeout    = 200 * time.Millisecond
    reaperGracePeriod = 500 * time.Millisecond
)
```

With `matchDeadline = 800 ms` and `batchDeadline = 200 ms`, the batch loop
now traverses up to **4 batches per ride** — which is exactly how many
5-driver batches fit into the 20-driver `GEORADIUS` result. Progressive
batching is progressive again.

`BatchDeadline` is exported (capitalized) so the invariant
`matchDeadline > BatchDeadline` is visible to the caller and any future
tuning notices the relationship.

---

## 5. Why 800 / 200 / 300

Rough numbers, not benchmarked:

- **`batchDeadline = 200 ms`**: 5 goroutines race on Redis SETNX. A local
  Redis SETNX RTT is single-digit ms in the median; under stress p99 can
  be tens of ms. 200 ms gives us ~10× median headroom, so we don't
  prematurely abort a batch whose winner is just a slow tick away.
- **`matchDeadline = 800 ms`**: `batchDeadline × 4` — enough to traverse
  every driver returned by `GEORADIUS` with 20-count. If the hotspot is
  saturated across all 20 nearby drivers, we honestly *should* fail;
  falling back to a larger radius would be a separate design choice.
- **`publishDeadline = 300 ms`**: Kafka `WriteMessages` with
  `RequiredAcks: kafka.RequireOne` + `BatchTimeout: 10 ms` completes in
  <100 ms comfortably. 300 ms gives ~3× headroom.
- **End-to-end p99 upper bound**: 800 + 300 = 1.1 s. That's above the
  README's "sub-second average" claim, but average is not p99 —
  average will be well under the sum of the deadlines because most
  matches win in batch 1 and publish in <50 ms.

These constants should be moved into config later (env / flag) so they can
be tuned per environment without a rebuild — flagged as follow-up, not
done here.

---

## 6. Interaction with the phantom-lock fix

The two fixes reinforce each other:

- The phantom-lock fix (see [`phantom-lock-fix.md`](phantom-lock-fix.md))
  changed the batch race so every successful SETNX has an explicit
  terminal fate — winner or released. That fix assumed batches would
  actually get to complete before the outer deadline killed them, which
  wasn't true under the 300 ms budget.
- The deadline fix ensures batches can actually run. Batches 2, 3, 4 now
  execute for the first time on hotspot rides. Each of them also
  produces phantom locks that need releasing — so
  `driver_lock_released_phantom_total` will climb more sharply after
  this fix, which is expected and correct.

If you see `driver_lock_released_phantom_total` growing much faster
post-deadline-fix, that is the phantom-lock guard doing more work because
progressive batching is now progressive. That is a good sign, not a
regression.

---

## 7. Failure modes to watch after the fix

- **Match latency histogram (`matching_latency_seconds`) will shift
  rightward for matches that fail**, because failed matches now spend up
  to 800 ms exhausting batches instead of failing fast at 300 ms. This
  is a correctness/coverage tradeoff, not a performance regression.
  Successful matches are unaffected — they still return on first
  winner.
- **Kafka backpressure**: the worker pool queue (200) x per-ride
  wall-clock (~800 ms on failure) means sustained failure floods can
  fill the queue faster than pre-fix, because we now hold each ride
  longer. The pool's blocking Submit will backpressure the consumer as
  designed — the more concerning symptom is Kafka consumer lag climbing.
  Not addressed here.
- **Nothing distinguishes match failure from publish failure**. There
  is still one collapsed error path: `_ = handler(event)` in
  `worker_pool.go`. Adding a `publish_failures_total` counter and
  separating the log lines would be a small follow-up.

---

## 8. Files changed

- `services/matching-service/internal/app/app.go` — replaced single
  `jobctx` with separate `matchCtx` (800 ms) and `publishCtx` (300 ms).
  Extracted `matchDeadline` and `publishDeadline` constants with the
  invariant documented at the top.
- `services/matching-service/internal/matching/matching.go` — reduced
  `batchDeadline` from 400 ms to 200 ms and exported it as
  `BatchDeadline` so the invariant against `matchDeadline` is
  discoverable to future readers.

No other services touched. `go vet ./...` and `go build ./...` from
`services/matching-service` succeed cleanly.

---

## 9. What this fix does not do

To keep the change tight:

- Deadlines are still hardcoded constants, not config.
- The worker pool still swallows handler errors (`_ = handler(event)`).
- The Kafka consumer's "retry on error" is still actually a silent drop
  (see architecture-review section F).
- The matching service still has no graceful shutdown — Ctrl+C kills it
  mid-commit.
- No metric yet distinguishes "match failed" from "publish failed".

Any of those can be picked up next.
