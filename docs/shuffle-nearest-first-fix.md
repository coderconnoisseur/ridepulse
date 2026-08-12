# Shuffle vs Nearest-First Fix — Matching Service

> **Status:** local-only note. Not intended for the public repo.
> Documents the shuffle-vs-nearest-first policy conflict in the matching
> loop, why the author's own "random offset + circular iteration" idea
> from `services/comment.md` would have been worse than the full shuffle,
> and the tier-preserving shuffle that resolves the tension.

---

## 1. The bug in one paragraph

`FindNearbyDrivers` asks Redis `GEORADIUS` for the nearest 20 drivers,
sorted ASC — nearest first. That ordering is the entire point of a
geospatial query: it tells the matcher which drivers should be tried
before which. Immediately after receiving that sorted list, the matcher
was doing a full-list `rand.Shuffle` and discarding it. The system was
paying for a geo-sorted query and throwing the sort away, meaning any
given ride was as likely to be assigned the 20th-nearest driver as the
1st-nearest — which, if you cared about pickup ETA, was the wrong ride-
hailing service to build.

The author's `services/comment.md` proposed replacing the shuffle with a
"random offset + circular iteration" instead. That would have been
worse: it retains the nearest-first *ordering* of Redis's response but
picks a *random point in the ordering* to start from — so at offset 15
you'd try the 15th-nearest driver before the 1st, exactly the failure
mode the shuffle was accidentally causing. This document explains why
neither pure policy works alone, and why a **tier-preserving shuffle**
does.

---

## 2. What the two candidate policies get wrong

### 2a. Pure nearest-first (no shuffle)

Every ride's `GEORADIUS` returns the sorted list. The batch loop takes
the top 5, races them, and moves on to 5-9 only if the first batch
fails. Under a **realistic** load — rides spread across the map — every
ride's "nearest 5" is a different set of drivers, contention is
naturally spread, and this policy is exactly what you want.

Under a **hotspot** — N concurrent rides at the same pickup coordinate,
which is exactly what the rider simulator produces today (`Pickup.Lat =
12.9`, `Pickup.Lng = 77.6`, fixed) — every ride's "nearest 5" is the
same 5 drivers. All N rides race to `SETNX` the same 5 driver locks.
1 wins, N-1 lose. All N-1 losers move to the same next-5 batch. Again
1 wins. This cascades until either a batch has enough spare drivers to
service all in-flight rides, or the outer match deadline fires and the
remaining rides fail.

The failure isn't a design flaw; it's the price of the "always
nearest" policy under a demand hotspot. In real dispatch systems you'd
mitigate it with surge pricing (reducing demand) or by expanding the
search radius — neither of which is on the current backlog. Within the
matching service, we can at least prevent the top-5 from being tried
in the same fixed order by every ride.

### 2b. Pure random shuffle (previous behaviour)

Rejects the geo sort entirely. Every ride's batch 1 is 5 uniformly
random drivers out of the top 20. Contention on any given driver is
reduced by a factor of 4, but expected pickup distance is drawn from
the whole top-20 population, which for a 50 km search radius means the
average assigned driver could be several kilometres farther than the
one Redis had already told us was nearest.

For a matching service that markets itself on "low-latency geospatial
matching," this is the wrong tradeoff.

### 2c. Random offset + circular iteration (comment.md's idea)

Same underlying problem as pure shuffle, worse implementation. Pick a
random start position `k ∈ [0, 20)`, then iterate `k, k+1, …, 19, 0,
1, …, k-1`. If `k = 15`, the first batch tries drivers 15-19 — the
farthest of the 20 — before ever touching drivers 0-14.

That's strictly worse than a shuffle: a shuffle at least mixes near and
far drivers into the first batch. The circular scheme deterministically
picks a **contiguous run of distance-adjacent drivers** starting from
a random offset, so the first batch's expected distance is worse than
random when the offset lands in the tail. This idea was flagged in
`comment.md` as an unfinished thought; leaving it here as the record of
why it wasn't adopted.

---

## 3. The fix: tier-preserving shuffle

Preserve the coarse nearest-first ordering, break the fine-grained
determinism. Divide the sorted driver list into **distance tiers**,
shuffle within each tier, keep the tier order intact.

```go
const driverTierSize = 10

func shuffleWithinTiers(drivers []string, tierSize int) {
    if tierSize < 2 {
        return
    }
    for i := 0; i < len(drivers); i += tierSize {
        end := i + tierSize
        if end > len(drivers) {
            end = len(drivers)
        }
        tier := drivers[i:end]
        rand.Shuffle(len(tier), func(x, y int) {
            tier[x], tier[y] = tier[y], tier[x]
        })
    }
}
```

For the current `Count:20` GEO query, this produces two tiers:

- **Tier 1** — positions 0..9 (the 10 nearest drivers).
- **Tier 2** — positions 10..19 (the next 10, farther).

Batches of 5 are then drawn from the shuffled list, top-down:

- **Batch 1** — 5 random drivers from tier 1 (nearest 10).
- **Batch 2** — the other 5 of tier 1.
- **Batch 3** — 5 random drivers from tier 2 (farther 10).
- **Batch 4** — the other 5 of tier 2.

Under **realistic** load (rides spread across the map): tier 1 for
each ride is a mostly-different set of drivers, so the shuffle is
effectively a no-op and pure nearest-first is recovered.

Under **hotspot** load: every ride's tier 1 is the same 10 drivers,
but which 5 land in *that ride's* batch 1 is random. Contention on
any given nearest-10 driver drops by a factor of ~2 (from
`concurrent_rides` to `concurrent_rides × 5/10`), and — crucially —
we never try a farther driver before a nearer one.

---

## 4. Contention math, quick sketch

Assume N concurrent rides at the same pickup. Redis GEO returns the
same top-20 sorted drivers for all of them.

| Policy                | Contention on driver at rank 0 | Expected assignment rank |
|-----------------------|--------------------------------|--------------------------|
| Pure nearest-first    | N                              | 0 (nearest)              |
| Pure random shuffle   | N × 5/20 = N/4                 | 9.5 (middle of top-20)   |
| Random offset+circular| N × 1/20 = N/20                | 9.5 (mean of ranks tried first) |
| **Tier shuffle (10)** | **N × 5/10 = N/2**             | **4.5 (middle of top-10)** |

Tier shuffle halves contention on the nearest driver compared to pure
nearest-first, and bounds assignments to the nearest-10 under normal
success, only escalating to tier 2 under sustained batch-1-and-2
failure. That's the intended behaviour: prefer nearby, but not at
the cost of throughput collapse under saturation.

---

## 5. Tests

Four unit tests were added in
`services/matching-service/internal/matching/matching_test.go`:

- `TestShuffleWithinTiersPreservesTierBoundaries` — 200 trials. Every
  driver that started in tier 1 (positions 0..9) is still in tier 1 after
  shuffling. Same for tier 2. If this ever regresses, the nearest-first
  invariant is silently broken and we've reverted to pure shuffle.
- `TestShuffleWithinTiersActuallyShuffles` — 100 trials. Guards against
  a broken implementation that no-ops (e.g., accidentally shuffling
  1-element windows).
- `TestShuffleWithinTiersHandlesRaggedLastTier` — 7 drivers, tier=5
  gives a full tier and a size-2 remainder. All 7 must still be present
  after the call.
- `TestShuffleWithinTiersTrivialTierSize` — `tierSize < 2` is a
  documented no-op; tests both `0` and `1`.

All four pass. These are the first `_test.go` files in the entire
repo.

---

## 6. Interaction with the earlier two fixes

The three fixes compose cleanly:

- **Phantom-lock fix**: every successful `SETNX` inside a batch is
  either the winner or is explicitly released, so trying more batches
  doesn't leak more phantoms.
- **Deadline fix**: 800 ms match budget with 200 ms per-batch means we
  actually get to run all 4 batches when needed.
- **Tier shuffle**: batches 1-2 stay within nearest-10, batches 3-4
  are the fallback into nearest-20. Neither batch tries farther drivers
  before nearer ones.

Together they turn "progressive parallel locking" into what the README
originally advertised: a bounded-concurrency search that stays near the
rider first, and only escalates outward under real contention.

---

## 7. Failure modes to watch after the fix

- `driver_lock_conflict_total` (existing metric): should drop
  meaningfully on hotspot workloads. If it doesn't, the workload is
  saturating tier 1 so hard that 10-way randomisation isn't enough —
  the next fix would be a larger `driverTierSize` (say 15 or 20), or
  the systemic fix: rider-simulator using a real geographic
  distribution.
- `matching_success_total`: expect a modest bump under hotspot load
  (fewer rides give up because they can only reach 5 contended
  drivers).
- Expected pickup distance is now bounded by `driverTierSize` under
  most matches; if you add a "matched driver rank" histogram later,
  you should see it concentrated in 0..9.

---

## 8. What this fix does not do

- Does not fix the rider simulator's fixed-point pickup coordinate
  (`Lat = 12.9, Lng = 77.6`), which is the ultimate reason hotspot
  contention dominates every measurement here. See architecture-
  review section D.
- Does not add a matched-driver-rank histogram to Prometheus.
- Does not consider expanding the search radius on cascade failure,
  which would be a natural next step for handling saturation.
- Does not adaptively size the tier — `driverTierSize = 10` is a
  hardcoded constant. In a real deployment you'd probably tune it per
  city or per demand density.

Any of those can be picked up next.

---

## 9. Files changed

- `services/matching-service/internal/matching/matching.go` — added
  `driverTierSize` constant, added `shuffleWithinTiers` helper,
  replaced the full-list `rand.Shuffle` call with a call into the
  tiered version. Extensive comments left in place to explain the
  policy tradeoff to future readers.
- `services/matching-service/internal/matching/matching_test.go` —
  **new file**. Four tests covering tier invariance, non-degeneracy,
  ragged last-tier, and trivial-tier-size no-op cases. First tests
  in the repo.

No other services touched. `go vet ./...`, `go build ./...`, and
`go test ./...` from `services/matching-service` all pass cleanly.
