# Driver State Model — matching-service and driver-simulator

> **Status:** local-only note. Not intended for the public repo.
> Documents the driver-assignment bug in the matching path, the Redis state
> model that replaced it, and how matching became idempotent under Kafka
> redelivery.

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
| `driver:lock:<driverID>` | string | rideID | 3 s | matching (`SET NX PX`) |
| `driver:assigned:<driverID>` | string | rideID | 10 min (safety net) | matching on assign; simulator deletes at trip end |
| `ride:driver:<rideID>` | string | driverID | 1 h | matching on assign |

### Lifecycle

```
free ──TryLockDriver──▶ locked ──AssignDriver──▶ assigned ──ReleaseDriver──▶ free
 │                        │                         │
 │ in GEO, no keys        │ in GEO, lock key        │ NOT in GEO, assigned key,
 │                        │ (value = rideID)        │ ride:driver record
 │                        └─ lock TTL / unlock ────▶ free
 └────────────────────────────────────────────────── (assigned TTL 10 min ──▶ free)
```

1. **free.** The driver is in `drivers:locations` and has no lock or
   assigned key. The simulator's location updates write the GEO position.
2. **locked.** `TryLockDriver` runs one Lua script: if
   `driver:assigned:<id>` exists it refuses (returns 0), otherwise
   `SET driver:lock:<id> <rideID> NX PX 3000`. The assigned check matters
   because a `GEORADIUS` result can be stale by the time the lock is
   attempted. Phantom locks (batch losers) are released with the
   compare-and-delete unlock script, as in
   [phantom-lock-fix.md](phantom-lock-fix.md).
3. **assigned.** `AssignDriver` runs one atomic Lua script, in this order:
   1. If `driver:lock:<id>` is not our rideID, return nil →
      `ports.ErrLockLost`. The matcher moves on to the next batch; the lock
      is no longer ours, so it is not released.
   2. If `ride:driver:<rideID>` already exists, drop our lock and return the
      existing driver untouched (replay, section 4).
   3. Otherwise `SET driver:assigned:<id> <rideID> EX 600`,
      `SET ride:driver:<rideID> <id> EX 3600`, `ZREM drivers:locations <id>`,
      `DEL driver:lock:<id>`.

   Because it is one script, there is no window in which a driver is both
   matchable and assigned, and no window in which the assignment exists
   without its idempotency record.
4. **released.** The driver simulator ends the trip (section 5) by deleting
   `driver:assigned:<id>` only if it still holds the same rideID. The next
   location update `GEOADD`s the driver back into the set.

If the assigned key is never released (simulator down), it expires after 10
minutes and the driver reappears with its next location update. The
`ride:driver` record outlives the assignment on purpose (1 h): it is what
makes a late redelivery recognizable after the driver is already free again.

---

## 4. Idempotent replay via `ride:driver:<rideID>`

`Match` begins with `GetRideDriver(rideID)`. If the record exists it
returns a `RideMatchedEvent` for that driver immediately (counting
`match_idempotent_replays_total`) and does no locking. This covers:

- a Kafka redelivery after a crash (the offset had not been flushed);
- a runner retry after `PublishRideMatched` failed, so the ride is assigned
  in Redis but `ride.matched` was never emitted;
- a retry after an infrastructure error that happened *after* the assign
  script ran but before its reply arrived. The script already created the
  record, so the retry finds it.

The assign script also covers the race in which two attempts for the same
ride both hold a lock: the second to run sees the record, frees its own
lock and returns the first attempt's driver.

Limit: the record lives for one hour. A redelivery older than that would
match again. Consumer lag is nowhere near that today.

### Timeouts and context

`AssignDriver` runs on a context detached from the match deadline
(`context.WithoutCancel`, then a 200 ms timeout). Without that, a match
that spent most of its 800 ms budget could have the assign cut off and the
caller would believe the ride failed while Redis might have assigned it.

Be honest about what the 200 ms bounds: the go-redis client is built with
default options, so `ContextTimeoutEnabled` is false. A context deadline
bounds waiting for a pooled connection, not the socket read or write; socket
I/O falls back to the client's own default timeouts (3 s read). The detached
200 ms is therefore a best-effort bound, not a hard one, and a stalled
Redis can hold a match handler for up to that longer socket timeout. If
that matters, enable `ContextTimeoutEnabled` on the client.

### Error handling around it

- `ErrLockLost` → try the next batch.
- Any other assign error → release our lock (fresh context), count a
  failure and return the error. matching-service's handler returns it, so
  the runner retries; the retry replays via the record if the assign had in
  fact landed.
- `ErrNoDriversAvailable` / `context.DeadlineExceeded` → business failure
  (offset committed, ride currently dropped; `ride.unmatched` is roadmap
  item 1c).

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
| `match_idempotent_replays_total` | Matches answered from `ride:driver:<rideID>`. |
| `matching_success_total`, `matching_failures_total` | Match outcomes; failures rise when free drivers run out. |
| `driver_lock_conflict_total` | Includes refusals of already-assigned drivers. |
| `nearby_drivers_current`, `nearby_driver_count` | Free drivers visible to a pickup; falls as drivers become busy. |
| simulator log: `trips completed /sec` | Rate at which drivers return to the pool. |

Redis spot checks:

```bash
redis-cli ZCARD drivers:locations          # free drivers (falls under load)
redis-cli --scan --pattern 'driver:assigned:*' | wc -l   # busy drivers
redis-cli GET ride:driver:<rideID>          # which driver a ride got
```

In steady state `ZCARD drivers:locations` plus the number of assigned keys
should be about `DRIVER_COUNT` (locked drivers are still in the set).

---

## 7. What remains

- **1c — `ride.unmatched`** for rides that find no driver.
- **1e — dedupe in assignment-service**, so a replayed `ride.matched` does
  not double-count.
- **Phase 2 throughput work.** With drivers now busy for the trip duration,
  the saturation point of the ladder moves; the supply model is also a new
  tuning dimension (`DRIVER_COUNT` against `DRIVER_TRIP_DURATION`).
- **Hard Redis time bounds** (`ContextTimeoutEnabled`), see section 4.
- **Benchmark ladder must be re-run** before any number is compared with the
  committed CSV.
