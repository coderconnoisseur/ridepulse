# Phantom Lock Fix — Matching Service

> **Status:** local-only note. Not intended for the public repo.
> Documents a correctness bug in `services/matching-service` that caused driver
> availability to silently degrade under contention, plus the fix applied.

---

## 1. The bug in one paragraph

Every ride, the matching service picked up to 5 candidate drivers at a time
and raced 5 goroutines to `SETNX driver:lock:<id>` (Redis) in parallel. It
kept the first success as the winner and threw the rest away. The problem: it
did not release the other successes. Any driver whose `SETNX` succeeded but
was not chosen as the batch winner remained locked in Redis for the full
TTL (3 s), unusable for any other ride. Under real load, 2–3 of the 5
racing goroutines often succeed at nearly the same time, so this
"drop-and-forget" pattern was continuously producing **phantom locks** —
locks with no ride behind them, sitting on drivers who should have been free.

The visible symptom the author noted in `services/comment.md` (contention
staying high, occasional latency spikes, "hot drivers getting hit
repeatedly") is what this bug looks like from the outside: the pool of
lockable drivers shrinks over time because more and more of them are in a
phantom-locked state, so the next ride hits contention even harder.

---

## 2. What the pre-fix code did

Source: `services/matching-service/internal/matching/matching.go` (before the fix).

Simplified:

```go
resultCh := make(chan string, 1)          // <-- capacity 1

for _, d := range batch {                 // batch has 5 drivers
    go func() {
        ok, _ := s.repo.TryLockDriver(batchCtx, d, event.RideID)
        if ok {
            select {
            case resultCh <- d:            // first arrival: succeeds
                cancel()
            default:                       // every other arrival: DROPPED
            }
        }
    }()
}

select {
case driverID := <-resultCh:
    return domain.RideMatchedEvent{DriverID: driverID, ...}, nil
case <-time.After(400 * time.Millisecond):
    cancel()
}
```

The dangerous path is the `default:` branch. When goroutine A wins the send,
`resultCh` is full (cap 1). Goroutines B, C, D, E — each of whom may have
just succeeded at `SETNX` — try to send, fall into `default:`, and return.
Nobody unlocks the driver those goroutines had just locked. Redis honours
the lock for 3 s. Repeat 300–500 times per second and driver availability
collapses.

---

## 3. Why the "cancel + TTL" defense doesn't help

Two claims are worth unpacking, because both were part of the original design
justification and neither is enough on its own:

**"`cancel()` will stop the losers before they SETNX."**
`cancel()` cancels `batchCtx`, but the losing goroutines may already be
past the `TryLockDriver` call by the time cancel runs. Redis `SETNX` also
completes on the server side even if the client cancels the request —
cancellation only prevents the client from *waiting*; the write already
happened. So cancel is a partial mitigation at best.

**"The 3 s TTL is a safety net."**
It is — but for the wrong failure mode. TTL exists to release locks after
service crashes or timeouts. Using it as the release mechanism for the
happy path means every phantom lock stays around for the full 3 s, during
which the driver contributes to Redis GEO's radius result (drivers get
returned by `GEORADIUS` regardless of lock state), so the next incoming
ride will burn a batch attempt trying to lock a driver that is *definitely*
already locked. That is exactly the contention pattern `comment.md`
describes.

---

## 4. Concrete failure scenario

Assume a hotspot (which is realistic here — the rider simulator sends every
ride from the same pickup, so `GEORADIUS` returns roughly the same 20
drivers for every ride).

Time `t = 0 ms`. Ride A begins matching. Batch = [D1, D2, D3, D4, D5].

- 5 goroutines fire `SETNX driver:lock:Dn "A"` in parallel.
- `t = 10 ms`: D1's `SETNX` succeeds → `resultCh <- "D1"` → succeeds. `cancel()`.
- `t = 11 ms`: D2's `SETNX` succeeds (already in flight, cancel arrives too late) → tries to send → `default` → returns.
- `t = 12 ms`: D3's `SETNX` succeeds → same → returns.
- `t = 13 ms`: D4's `SETNX` fails (someone else already locked it — this is the hotspot).
- `t = 14 ms`: D5's `SETNX` succeeds → same → returns.

**State in Redis:**

| Key                     | Value  | Expires in |
|-------------------------|--------|------------|
| `driver:lock:D1`        | `"A"`  | 3 s        |
| `driver:lock:D2`        | `"A"`  | 3 s (phantom) |
| `driver:lock:D3`        | `"A"`  | 3 s (phantom) |
| `driver:lock:D5`        | `"A"`  | 3 s (phantom) |

Ride A is matched with D1. D2, D3, D5 are locked to ride A but ride A does
not know about them and never will. They stay locked for 3 s.

Now ride B arrives at `t = 20 ms`. Its `GEORADIUS` returns the same 20
drivers, ordered by distance. Assume batch = [D1, D2, D3, D4, D5] again.
Every one of those `SETNX` calls fails. Batch times out at 400 ms without
finding a driver. Ride B moves to batch 2 — but with only 300 ms of job
budget (`app.go:47`) and 400 ms per batch, ride B has already exceeded
its deadline. Ride B fails.

This is the mechanism by which the phantom-lock leak actively causes
matching failures for *other* rides.

---

## 5. The fix

**File: `services/matching-service/internal/matching/matching.go`** — rewritten
`tryLockBatch` around three principles:

1. **No successful SETNX is ever silently dropped.** The result channel is
   now buffered to `len(batch)` (was 1). Every goroutine that succeeds gets
   its `driverID` onto the channel without ever taking a `default` branch.

2. **Exactly one lock is retained. Every other lock is explicitly released.**
   The first arrival on the channel becomes the winner. All subsequent
   arrivals — whether observed before we return, or in flight and arriving
   later — are handed to a reaper goroutine that runs `UnlockDriver`
   against each of them.

3. **Release uses a fresh context.** The batch context is cancelled the
   moment we pick a winner, but the release path must not be cancelled —
   otherwise we'd cancel the very operation whose job is to fix the leak.
   `releaseLock` derives a new context from `context.Background()` with a
   200 ms cap.

**File: `services/matching-service/internal/redis/driver_repository.go`** — new
`UnlockDriver(driverID, rideID)` method backed by a Redis Lua script:

```lua
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
```

The script is essential. A naïve `DEL` would risk deleting a lock that had
already expired and been reacquired by a different ride. The GET-then-DEL
runs atomically inside Redis, so we can only ever delete a lock whose value
still matches the rideID we're trying to release.

**File: `services/matching-service/internal/ports/redis.go`** — added
`UnlockDriver` to the `DriverRepository` interface.

**File: `services/matching-service/internal/metrics/metrics.go`** — added
`driver_lock_released_phantom_total` counter, incremented on every
successful phantom release. This is how we verify the fix is actually
doing work under load: the counter should be non-zero on every real
contention run, and it should scale with lock success rate.

---

## 6. Post-fix run of the same scenario

Same setup: ride A, batch = [D1..D5], D1/D2/D3/D5 succeed at SETNX.

- All 5 goroutines send onto `resultCh` (or return without sending, if their
  SETNX failed).
- Outer loop reads first arrival → say D1 → sets that as the winner,
  cancels the batch context, hands `resultCh` to `reapBatch`, returns.
- `reapBatch` drains D2, D3, D5 (as they arrive on the channel) and calls
  `UnlockDriver` for each one. Each call runs the Lua script. Each lock is
  released.
- `driver_lock_released_phantom_total` increments by 3.

**State in Redis after ride A returns:**

| Key                     | Value  | Expires in |
|-------------------------|--------|------------|
| `driver:lock:D1`        | `"A"`  | 3 s        |

Ride B arrives. D2, D3, D5 are free. Ride B matches D2 quickly. No cascade.

---

## 7. Reaper semantics and bounds

`reapBatch` is bounded by two things:

- The result channel is closed when all batch goroutines are done
  (`wg.Wait(); close(resultCh)`). Reaper exits cleanly on channel-closed.
- If a goroutine gets stuck in Redis for longer than the reaper's grace
  period (500 ms currently), the reaper bails and the corresponding lock
  falls back to the 3 s TTL. This is strictly no worse than the pre-fix
  behaviour, which took the TTL path for *every* extra lock in *every*
  batch, not just the rare wedged one.

Concurrent reaper goroutines are proportional to the failure rate of
batches (only failed / winner-selected batches spawn a reaper), each bounded
in lifetime by the grace period. There is no unbounded goroutine growth.

---

## 8. Edge cases considered

- **All 5 SETNX calls fail.** `wg.Wait(); close(resultCh)`. The main select
  reads `!more`, returns `("", false)`. No reaper spawned (nothing to reap).
  Correct.

- **Only 1 SETNX succeeds.** Main select reads it as winner, cancels,
  spawns reaper. Reaper drains the channel — which closes without another
  send — and exits. `DriverLockReleasedPhantom` does not increment. Correct.

- **Batch times out with no winner but SETNX succeeds after cancel.** The
  in-flight goroutine still writes to `resultCh` (buffered). Reaper picks
  it up and releases. Correct.

- **Parent context (job deadline) fires mid-batch.** Same as timeout: cancel,
  reap. If a straggler succeeds during the reaper's grace period, it's
  released. If it succeeds *after* the reaper exits, TTL cleans it up
  within 3 s. Correct.

- **Redis is unreachable when we try to release.** `UnlockDriver` returns
  an error, `releaseLock` skips the metric increment, and the lock falls
  back to TTL. Correct — we never treat a failed release as a success.

- **Lock TTL expires and driver gets re-locked by a different ride
  between our SETNX and our attempted release.** The Lua script's `GET`
  will not match our `rideID`, so `DEL` is not executed. Correct — we
  never delete someone else's lock.

---

## 9. How to see the fix working

Under a real contention run, watch these Prometheus metrics:

```
driver_lock_success_total          # locks acquired
driver_lock_released_phantom_total # of those, how many we had to release
driver_lock_conflict_total         # SETNX rejections
matching_success_total             # rides matched
matching_failures_total            # rides not matched
```

Expected shape post-fix on a hotspot workload (rider-simulator's default
one-point-per-ride pattern):

- `driver_lock_released_phantom_total > 0` and rising steadily. If it is
  always zero even under load, the fix is not being exercised — most
  likely the workload has too little concurrency in each batch to produce
  multiple wins.
- `matching_success_total` sustained higher over time than pre-fix,
  because ride N+1 no longer inherits ride N's phantom-locked drivers.
- `matching_failures_total` "no drivers available" curve flatter, not
  climbing.

If those trends do not appear, the next likely bottleneck is not the
phantom lock — it is one of the other items in the review file (300 ms job
deadline vs 400 ms batch deadline conflict; Kafka single-partition writes;
hardcoded pickup coordinates producing an artificial hotspot).

---

## 10. What this fix does not do

To keep the change tightly scoped, this fix does **not** address any of the
following, all of which are still open:

- The 300 ms `jobctx` deadline (in `app.go`) vs 400 ms per-batch deadline
  (in `matching.go`) — progressive batching still effectively degenerates
  to a single batch because the outer deadline dominates.
- The `math/rand`'s `rand.Shuffle` reordering `GEORADIUS`'s
  nearest-first result — nearest-first is still discarded on every ride.
- The rider simulator's fixed pickup location that concentrates all demand
  onto the same ~20 drivers, so the "contention" being measured is
  synthetic.
- `ride.matched` still has no consumer — successful matches produce no
  downstream effect beyond a Prometheus counter.
- Consumer "retry" that is actually a silent drop (see the arch review
  section F).

Any of those can be picked up next.

---

## 11. Files changed

- `services/matching-service/internal/matching/matching.go` — rewrote
  `tryLockBatch`, added `releaseLock`, `reapBatch`. Constants extracted.
- `services/matching-service/internal/redis/driver_repository.go` — added
  `unlockScript` (Lua) and `UnlockDriver` method.
- `services/matching-service/internal/ports/redis.go` — added
  `UnlockDriver` to the `DriverRepository` interface.
- `services/matching-service/internal/metrics/metrics.go` — added
  `DriverLockReleasedPhantom` counter and registered it.

No other services touched. `go build ./...` from
`services/matching-service` succeeds cleanly.
