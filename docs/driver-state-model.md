# Driver State Model — matching-service and driver-simulator

---

## 1. The bug in one paragraph

Before this change, matching ended at `SETNX driver:lock:<id>`. The lock
expired after 3 seconds and nothing recorded that the driver now had a ride.
The driver stayed in the `drivers:locations` GEO set, and the driver
simulator kept re-publishing its location, so the same driver became
matchable again as soon as the lock lapsed: one driver could be handed
several rides. Separately, nothing made `Match` safe to run twice for the
same ride, so with at-least-once Kafka consumers (see
[at-least-once-consumers.md](at-least-once-consumers.md)) a retry or
redelivery would lock and assign a *second* driver to a ride that already
had one.

---

## 2. Root cause

The lock was being used as the assignment. A lock is a short-lived mutual
exclusion token for the matching race; an assignment is a durable fact that
lasts for the length of a trip. They need different keys, different TTLs and
an owner (the simulator) that ends the trip. Also, "find nearby drivers" and
"take a lock" were the only steps, so there was nowhere to put a per-ride
record to make a replay recognizable.

---

## 3. The state model

Redis keys. matching-service and driver-simulator share them; do not rename.

| Key | Type | Value | TTL | Written by |
|---|---|---|---|---|
| `drivers:locations` | GEO set | driver positions | none | simulator (`GEOADD`), matching (`ZREM` on assign) |
| `driver:assigned:<driverID>` | string | rideID | 10 min (safety net) | matching on assign; simulator deletes at trip end |
| `ride:outcome:<rideID>` | hash | `driver` and `claimed_at`, or `unmatched_at` | 1 h matched, 10 min unmatched | matching, in the claim script |

### Lifecycle

```
free ──claim script──▶ assigned ──ReleaseDriver──▶ free
 │                        │
 │ in GEO, no keys        │ NOT in GEO, assigned key, ride:outcome record
 └────────────────────────┴── (assigned TTL 10 min ──▶ free)
```

1. **free.** The driver is in `drivers:locations` and has no assigned key.
   The simulator's location updates write the GEO position.
2. **assigned.** The claim script runs as one atomic step:
   1. If `ride:outcome:<rideID>` exists, return that same outcome and change
      nothing (replay, section 4).
   2. Otherwise take the nearest drivers within the radius from
      `drivers:locations`, and for the first without a
      `driver:assigned:<id>` key: `SET driver:assigned:<id> <rideID> EX 600`,
      `HSET ride:outcome:<rideID> driver <id> claimed_at <now>` (1 h),
      `ZREM drivers:locations <id>`.
   3. If none is free, `HSET ride:outcome:<rideID> unmatched_at <now>` (10 min)
      and return nil. The ride is unmatched, and that is final.

   Because it is one script, two rides can't claim one driver, there is no
   window in which a driver is both matchable and assigned, and no window in
   which the assignment exists without its idempotency record. The earlier
   lock-based design and why it was replaced are covered in
   [matching-under-load.md](matching-under-load.md).
3. **released.** The driver simulator ends the trip (section 5) by deleting
   `driver:assigned:<id>` only if it still holds the same rideID. The next
   location update `GEOADD`s the driver back into the set.

If the assigned key is never released (simulator down), it expires after 10
minutes and the driver reappears with its next location update. The
`ride:outcome` record outlives the assignment on purpose (1 h): it is what
makes a late redelivery recognizable after the driver is already free again.

---

## 4. Idempotent replay via `ride:outcome:<rideID>`

The claim script begins by reading `ride:outcome:<rideID>`. If the record exists
it returns that outcome and changes nothing: the same driver with the original
claim time (counting `match_idempotent_replays_total`), or no driver. This covers:

- a Kafka redelivery after a crash (the offset had not been flushed);
- a runner retry after `PublishRideMatched` failed, so the ride is assigned
  in Redis but `ride.matched` was never emitted;
- a retry after a timeout that happened *after* the claim script ran but
  before its reply arrived. The script already created the record, so the
  retry finds it.

Limit: the record lives for one hour if matched and ten minutes if unmatched.
The shorter TTL keeps Redis memory bounded, because unmatched rides can arrive
much faster under overload. A redelivery older than that would be decided again.
Consumer lag was nowhere near that in testing.

### Timeouts and context

A claim runs on a context detached from caller cancellation
(`context.WithoutCancel`) with a timeout of `MATCH_DEADLINE` (default 500 ms),
and the Redis client has `ContextTimeoutEnabled` set so that the timeout also
covers socket reads and writes. If a claim times out, its outcome is unknown:
the script may have run with the reply lost. So the error is retried rather
than reported as unmatched, and the retry replays through `ride:outcome`.

### Error handling around it

- No driver → `ErrNoDriversAvailable`: matching publishes `ride.unmatched` and
  commits the offset (section 4b).
- Any other error, including a timeout → returned to the runner, which retries
  and then dead-letters. A retry replays the outcome if the script had run.

### 4b. The unmatched path

When matching ends without a driver, the handler publishes a
`ride.unmatched` event instead of dropping the ride:

| Field | Meaning |
|---|---|
| `RideID` | The ride (also the Kafka key). |
| `Reason` | `no_drivers` (`ErrNoDriversAvailable`). |
| `RequestedAt` | UnixNano stamp from the gateway, carried through for latency. |
| `FailedAt` | UnixNano time the failure was decided. |

Publishing has its own 2 s budget. If
the publish fails the handler returns the error, the runner retries it and
eventually dead-letters the `ride.priced` message, as for any other
infrastructure failure; the offset is committed only after the event is out.
`match_unmatched_published_total{reason}` counts successful publishes. The
writer is created with `AllowAutoTopicCreation`, so the first unmatched ride
creates the topic; assignment-service dead-letters undecodable events to
`ride.unmatched.dlq`.

An unmatched ride writes only its `ride:outcome` record: no assigned key. So a
redelivered `ride.priced` for an unmatched ride stays unmatched, even if drivers
have since been released, and publishes the same `ride.unmatched` again.

assignment-service ignores a second `ride.unmatched` for the same ride
(`unmatched_duplicates_total`). It also treats matched as final, so a
`ride.matched` replaces an earlier unmatched record. That shouldn't happen now
unless the 10-minute record has expired.

assignment-service counts unmatched rides in `unmatched_total{reason}` and
`unmatched_end_to_end_latency_seconds`, separately from `assignments_total`
and `end_to_end_latency_seconds`, which now count each matched ride once
(redeliveries go to `assignment_duplicates_total`).

---

## 5. The driver simulator

`DRIVER_COUNT` (default 1000) drivers each tick every 500 ms. A tick runs
one Lua script (`UpdateDriverLocation`): if `driver:assigned:<id>` exists it
returns the rideID and skips the `GEOADD`; otherwise it records the
position. So an assigned driver does not re-enter the GEO set behind
matching's back.

When the simulator sees an assignment it starts a per-driver trip clock. When
`DRIVER_TRIP_DURATION` (Go duration, default `5s`) has elapsed, it calls
`ReleaseDriver`, a compare-and-delete on `driver:assigned:<id>` with the
rideID it observed, and counts a completed trip. A release error is logged
once and retried on the next tick. The trip clock starts when the simulator
first observes the assignment, so a driver stays busy for the trip duration
plus up to one tick (500 ms). If the key disappears or changes (TTL, a new
ride), the local state resets.

Other configuration: `REDIS_ADDR` (default `localhost:6379`). The
simulator exits cleanly on SIGINT/SIGTERM and logs `Drivers updates /sec`
and `trips completed /sec` once a second.

**Consequence for benchmarking:** drivers are now genuinely busy for the
trip duration, so supply is finite and the old benchmark numbers are not
comparable. See the README.

---

## 6. How to observe it

| Signal | Meaning |
|---|---|
| `match_idempotent_replays_total` | Matches answered from `ride:outcome:<rideID>`. |
| `matching_success_total`, `matching_failures_total` | Match outcomes; failures rise when free drivers run out. |
| `driver_claim_latency_seconds` | Time for one claim, as seen by the client. |
| simulator log: `trips completed /sec` | Rate at which drivers return to the pool. |

Redis spot checks:

```bash
redis-cli ZCARD drivers:locations          # free drivers (falls under load)
redis-cli --scan --pattern 'driver:assigned:*' | wc -l   # busy drivers
redis-cli HGETALL ride:outcome:<rideID>     # what happened to a ride
```

In steady state `ZCARD drivers:locations` plus the number of assigned keys
should be about `DRIVER_COUNT`.

---

## 7. What remains

- **Persistent outcome store.** assignment-service dedupes in memory, so a
  restart forgets which rides it has seen.
- **Phase 2 throughput work.** With drivers now busy for the trip duration,
  the saturation point of the ladder moves; the supply model is also a new
  tuning dimension (`DRIVER_COUNT` against `DRIVER_TRIP_DURATION`).
- **Late announcements after a crash.** A claimed ride can be announced about
  30 s late, possibly after its simulated trip ended. See the README.
