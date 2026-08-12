# RPS Benchmarking Setup — RidePulse

> **Status:** local-only note. Not intended for the public repo.
> Documents everything that had to change so the pipeline could be
> loaded and measured honestly, plus how to actually run a benchmark
> and interpret the results.

---

## 1. What was blocking a real RPS benchmark

Before this pass, "running a load test" meant pointing `rider-simulator`
at `api-gateway`, watching the simulator's stdout, and guessing.
Concretely, the following was broken or missing:

| Problem                                                    | Impact on a benchmark                                     |
|------------------------------------------------------------|-----------------------------------------------------------|
| `ride.matched` had no consumer                             | No end-to-end success signal — matches produced no observable effect. |
| No timestamp propagated through the event chain            | No way to measure end-to-end latency separately from local per-service latency. |
| `rider-simulator` sent every ride from the exact same coordinates | Every `GEORADIUS` returned the same top-20 drivers → artificial hotspot dominated every measurement. |
| Only `matching-service` exposed Prometheus metrics         | Blind spots at every other stage — impossible to see where load actually broke first. |
| No Prometheus scraper                                      | `/metrics` was exposed but nothing was collecting it.     |
| `api-gateway` handler logged `"1"`, `"2"` per request      | Under 300+ RPS, stdout writes became a bottleneck.        |

All of these are addressed here.

---

## 2. What was added / changed

### 2a. New service: `assignment-service`

The terminal consumer of the pipeline. Sits on Kafka topic `ride.matched`,
stores each assignment in-memory keyed by ride ID, and emits three
metrics on port `:2114`:

- `assignments_total` (counter)
- `end_to_end_latency_seconds` (histogram — from `ride.requested`
  emission to `ride.matched` consumption)
- `assignment_missing_requested_at_total` (counter — should be zero;
  non-zero indicates schema drift in the event chain)

It also exposes two debug HTTP endpoints for verifying pipeline
correctness during a benchmark:

- `GET /assignments/<rideID>` — returns the stored assignment (JSON) or 404.
- `GET /count` — plaintext count of assignments observed so far.

Layout:

```
services/assignment-service/
├── cmd/assignment-service/main.go
├── go.mod
└── internal/
    ├── app/       app.go               (wires everything)
    ├── domain/    ride.go              (RideMatchedEvent, Assignment)
    ├── httpapi/   server.go            (/metrics, /assignments/, /count)
    ├── kafka/     consumer.go          (matches other services' consumer shape)
    ├── metrics/   metrics.go
    └── store/     memory.go            (sync.RWMutex-guarded map)
```

Added to `services/go.work`.

### 2b. `RequestedAt` propagated through the event chain

The domain type in every service now carries a `RequestedAt int64` field
(UnixNano) stamped at api-gateway when the HTTP POST arrives:

- `api-gateway` — `RideRequestedEvent.RequestedAt = time.Now().UnixNano()`
  in the `/rides` handler.
- `pricing-service` — `ComputePrice` copies `RequestedAt` from
  `RideRequestedEvent` into `RidePricedEvent`.
- `matching-service` — the `Match` function copies `RequestedAt` from
  `RidePricedEvent` into `RideMatchedEvent`.
- `assignment-service` — subtracts `RequestedAt` from `time.Now()` on
  consumption; records into `end_to_end_latency_seconds`.

Case-insensitive JSON unmarshal absorbs the pricing-service `RideId` vs.
`RideID` inconsistency without changes on that axis (still a follow-up).

### 2c. Rider simulator: bounding-box sampling

`services/rider-simulator/internal/simulator/simulator.go`: pickup and
drop are now each sampled uniformly from a 6 km × 6 km box centred on
the driver cluster (`[12.88, 12.94] × [77.58, 77.64]`). This overlaps
the driver-simulator's spawn box (`[12.9, 12.92] × [77.6, 77.62]`) so
every ride still finds candidates in Redis GEO — but rides no longer
pile onto the same top-5 nearest drivers.

Combined with the earlier tier-shuffle fix, hotspot contention is now
proportional to actual demand density, not an artefact of the
simulator.

### 2d. `api-gateway` metrics

New file: `services/api-gateway/internal/metrics/metrics.go` with:

- `gateway_requests_total` (counter)
- `gateway_request_duration_seconds` (histogram, 5 ms–2 s buckets)
- `gateway_publish_errors_total` (counter)
- `gateway_bad_requests_total` (counter)

Instrumented in `handlers/rides.go`. Exposed at `/metrics` on the same
`:8080` server as `/rides` (via `gin.WrapH(promhttp.Handler())` in
`server/server.go`).

Debug `log.Println("1", …)` / `log.Println("2", …)` calls were removed —
under 300+ RPS those alone were producing thousands of log lines per
second and skewing latency. `gin.Logger()` middleware was also removed
for the same reason. Panics are still caught by `gin.Recovery()`.

### 2e. `pricing-service` metrics

New file: `services/pricing-service/internal/metrics/metrics.go` with:

- `pricing_events_consumed_total` (counter)
- `pricing_events_published_total` (counter)
- `pricing_publish_errors_total` (counter)
- `pricing_processing_duration_seconds` (histogram, 1 ms–1 s buckets)

Metrics HTTP server exposed on `:2115` using a dedicated `ServeMux`
(deliberately not `DefaultServeMux`, so it can't collide with anything
else that happens to register a handler on the default mux).

### 2f. Prometheus scraper

- `deployment/prometheus.yml` — scrape config for all four services.
- `deployment/docker-compose.yml` — new `prometheus` service (image
  `prom/prometheus:v2.55.1`), volume-mounts the config, port-forwards
  `:9090`, `extra_hosts` entry so `host.docker.internal` resolves to
  the host on Linux the way it already does on Docker Desktop for
  Windows / macOS.

Scrape targets:

| Job                  | Target                              |
|----------------------|-------------------------------------|
| `api-gateway`        | `host.docker.internal:8080`         |
| `pricing-service`    | `host.docker.internal:2115`         |
| `matching-service`   | `host.docker.internal:2112`         |
| `assignment-service` | `host.docker.internal:2114`         |

---

## 3. Running a benchmark

### 3a. Start the infra

```bash
cd deployment
docker-compose up -d
```

Verify:

- Zookeeper: port 2181
- Kafka: port 9092
- Redis: port 6379
- Prometheus UI: http://localhost:9090

### 3b. Start the four services (one terminal each, on the host)

The services do NOT run in Docker in this setup — Prometheus reaches them
via `host.docker.internal`. Order does not matter, but it's easier to
tail the logs of each in its own terminal.

```bash
cd services/matching-service && go run cmd/matching-service/main.go
cd services/assignment-service && go run cmd/assignment-service/main.go
cd services/pricing-service && go run cmd/pricing-service/main.go
cd services/driver-simulator && go run cmd/driver-simulator/main.go
cd services/api-gateway && go run cmd/api-gateway/main.go
```

Sanity-check each exposes metrics:

```bash
curl -s http://localhost:8080/metrics | head
curl -s http://localhost:2112/metrics | head
curl -s http://localhost:2114/metrics | head
curl -s http://localhost:2115/metrics | head
```

Confirm Prometheus is scraping all four at http://localhost:9090/targets .
Every job should show `state=up`.

### 3c. Send load (one-shot, automated)

The `tools/benchmark` runner walks a configurable RPS ladder, drives
`rider-simulator` per step, samples Prometheus at the end of each
measurement window, and writes a CSV row per step.

```bash
cd tools/benchmark
go run cmd/benchmark/main.go
```

Default ladder is `50:20,100:40,200:80,300:120,500:200,800:300`
(RPS:concurrency pairs). Default warmup 15 s, window 30 s, cooldown 10 s
— total 5–6 min wall clock. Override any of them:

```bash
go run cmd/benchmark/main.go \
  --ladder=100:40,300:120,600:250 \
  --warmup=20s --window=45s --cooldown=15s \
  --output=my-run.csv
```

The runner:

1. Compiles `rider-simulator` once at start (so per-step startup is
   fast).
2. For each step, spawns rider-sim with `RIDER_RPS=<rps>`
   `RIDER_CONCURRENCY=<concurrency>` env vars.
3. Waits **warmup** for the ramp-up + Prometheus scrapes to catch up
   (Prometheus scrape interval is 5 s; you need ≥10 s of steady state
   in the window).
4. Waits **window**. That's the measurement period.
5. Queries Prometheus for rates and histogram quantiles over the same
   window duration.
6. Kills rider-sim and waits **cooldown** before the next step
   (drains in-flight rides so their metrics don't leak into the next
   window).

Ctrl+C at any point cleanly kills the running rider-sim before
exiting.

Terminal output during a run:

```
=== step 3/6: target 200 RPS, concurrency 80 ===
... measurement window 30s ...
step 3 done: target=200 rps → in=198.7 rps, assign=196.4 rps
   (success=98.8%), e2e p95=142 ms, match p95=98 ms,
   lock_conflicts=12.3/s, phantom_rel=1.1/s
```

Stop at the step where either match success rate drops below ~95 % or
end-to-end p99 breaches your SLO — that's the ceiling.

#### CSV columns

| Column                    | Meaning                                                                 |
|---------------------------|-------------------------------------------------------------------------|
| `step`                    | 1-indexed step number in the ladder                                     |
| `target_rps`              | Requested RPS for this step                                             |
| `target_concurrency`      | Max in-flight riders                                                    |
| `actual_rps_in`           | `rate(gateway_requests_total[window])` — real arrival rate at the gateway |
| `assignments_rps`         | `rate(assignments_total[window])` — end-to-end success rate             |
| `match_success_rps`       | `rate(matching_success_total[window])` — matcher success rate (before publish) |
| `match_failures_rps`      | `rate(matching_failures_total[window])`                                 |
| `publish_errors_rps`      | `rate(gateway_publish_errors_total[window])` — Kafka pushback signal    |
| `lock_conflicts_rps`      | `rate(driver_lock_conflict_total[window])` — SETNX rejections           |
| `phantom_releases_rps`    | `rate(driver_lock_released_phantom_total[window])` — phantom-lock guard activity |
| `e2e_p{50,95,99}_ms`      | `histogram_quantile` on `end_to_end_latency_seconds_bucket`, in ms      |
| `gateway_p95_ms`          | `histogram_quantile(0.95)` on `gateway_request_duration_seconds`, in ms |
| `match_p95_ms`            | `histogram_quantile(0.95)` on `matching_latency_seconds`, in ms         |
| `success_rate_pct`        | `100 × assignments_rps / actual_rps_in`, clamped to 100                 |

#### Manual run (if you don't want the CSV)

If you'd rather drive rider-sim by hand and watch Prometheus live:

```bash
cd services/rider-simulator
RIDER_RPS=300 RIDER_CONCURRENCY=120 go run cmd/rider-simulator/main.go
```

`RIDER_URL` (defaults `http://localhost:8080/rides`) is the third
env-var handle.

### 3d. Verify the pipeline is closed

```bash
# Total assignments the terminal consumer has seen
curl -s http://localhost:2114/count

# Fetch a specific ride's assignment
curl -s http://localhost:2114/assignments/<rideID> | jq
```

If `/count` is much lower than the number of `POST /rides` you sent,
the pipeline is dropping messages somewhere — check the metrics
below to localize where.

---

## 4. PromQL queries for interpretation

### 4a. End-to-end pipeline throughput

```
rate(gateway_requests_total[1m])
rate(pricing_events_published_total[1m])
rate(matching_success_total[1m])
rate(assignments_total[1m])
```

These four should be roughly equal in steady state. Any gap between
adjacent rates points to a stage that's dropping or falling behind.

### 4b. End-to-end latency percentiles

```
histogram_quantile(0.50, sum by (le) (rate(end_to_end_latency_seconds_bucket[1m])))
histogram_quantile(0.95, sum by (le) (rate(end_to_end_latency_seconds_bucket[1m])))
histogram_quantile(0.99, sum by (le) (rate(end_to_end_latency_seconds_bucket[1m])))
```

Includes gateway handler + Kafka publish + pricing + Kafka publish +
matching + Kafka publish + assignment-service consumption. If this
diverges sharply from `matching_latency_seconds`, the tail lives
outside the matcher — usually Kafka.

### 4c. Per-stage latency

```
histogram_quantile(0.95, sum by (le) (rate(gateway_request_duration_seconds_bucket[1m])))
histogram_quantile(0.95, sum by (le) (rate(pricing_processing_duration_seconds_bucket[1m])))
histogram_quantile(0.95, sum by (le) (rate(matching_latency_seconds_bucket[1m])))
```

### 4d. Match failure attribution

```
rate(matching_failures_total[1m])
rate(driver_lock_conflict_total[1m])
rate(driver_lock_released_phantom_total[1m])
```

- Rising `driver_lock_conflict_total` with flat `matching_success_total`
  → hotspot / demand exceeds supply. Not a code bug; either sparser
  pickups or more drivers.
- Rising `driver_lock_released_phantom_total` → phantom-lock guard is
  actively doing work. Good sign, expected under contention.
- Rising `matching_failures_total` with high `driver_lock_conflict_total`
  → the outer match deadline (800 ms) isn't enough to traverse all four
  batches for these rides. Consider larger radius or fewer batches
  before failing.

### 4e. Publish-error attribution

```
rate(gateway_publish_errors_total[1m])
rate(pricing_publish_errors_total[1m])
```

Any non-zero value here means Kafka is the bottleneck. The `RequireOne`
ack policy and `10 ms` batch timeout are what you'd tune first.

---

## 5. Interpreting what you'll see

### 5a. Expected shape at low load (~50 RPS)

- All four `rate(*_total[1m])` curves overlap.
- End-to-end p95 ≈ 50–150 ms.
- `driver_lock_conflict_total` near zero.
- `driver_lock_released_phantom_total` near zero.

### 5b. Expected shape as load climbs

- Batch 1 lock conflicts start showing (`driver_lock_conflict_total`
  rises linearly with RPS).
- Phantom-lock releases rise proportionally (the fix is working).
- End-to-end p95 climbs as batches 2 / 3 / 4 get exercised.
- Match success rate stays close to 100 % — the whole point of the
  deadline + phantom + shuffle fix stack.

### 5c. Expected failure mode at the ceiling

You will very likely hit one of these first:

1. **Kafka publish latency spikes** — single-partition topics + one
   producer per service means Kafka's ack loop becomes the tail.
   Fix: multi-partition topics + explicit topic creation (not covered
   here, listed in Section 7).
2. **Redis GEOADD throughput** — the driver-simulator does 2000 GEOADDs/s
   at 1000 drivers × 500 ms tick. Under matcher load, GEORADIUS queries
   compete with those writes on a single Redis connection. Fix:
   pipeline the GEOADDs and/or use a Redis pool.
3. **Windows ticker granularity in rider-simulator** — `time.Second /
   1000 = 1 ms` ticker won't fire that fast on Windows (~15 ms
   resolution). At requested-RPS > ~600 you'll see actual RPS plateau
   even though the code says otherwise.

Whichever of these shows up first is your real first bottleneck.
Everything downstream that "seemed" slow was masked by it.

---

## 6. Files changed

**New:**

- `services/assignment-service/` — entire new module (7 files)
- `services/api-gateway/internal/metrics/metrics.go`
- `services/pricing-service/internal/metrics/metrics.go`
- `tools/benchmark/` — automated ladder runner (`go.mod` + single-file
  `cmd/benchmark/main.go`)
- `deployment/prometheus.yml`
- `docs/rps-benchmarking-setup.md` (this file)

**Modified:**

- `services/go.work` — added `./assignment-service`
- `services/api-gateway/go.mod` — added prometheus client (tidied)
- `services/api-gateway/internal/domain/ride.go` — `RequestedAt` field
- `services/api-gateway/internal/handlers/rides.go` — stamp
  `RequestedAt`, instrument metrics, remove debug logging
- `services/api-gateway/internal/server/server.go` — register metrics,
  expose `/metrics`, drop `gin.Logger()` middleware
- `services/pricing-service/go.mod` — added prometheus client (tidied)
- `services/pricing-service/internal/domain/ride.go` — `RequestedAt` on
  both events
- `services/pricing-service/internal/pricing/pricing.go` — propagate
  `RequestedAt`
- `services/pricing-service/internal/app/app.go` — metrics HTTP server
  on `:2115`, instrument consume/publish paths
- `services/matching-service/internal/domain/ride.go` — `RequestedAt`
  on both events
- `services/matching-service/internal/matching/matching.go` — propagate
  `RequestedAt`
- `services/rider-simulator/cmd/rider-simulator/main.go` — reads
  `RIDER_URL`/`RIDER_RPS`/`RIDER_CONCURRENCY` env vars (backwards-compat
  defaults preserved) so the benchmark runner can drive it per step
- `services/rider-simulator/internal/simulator/simulator.go` —
  bounding-box pickup/drop sampling
- `deployment/docker-compose.yml` — added `prometheus` service

`go build ./...` and `go test ./...` pass cleanly across every module.

---

## 7. Deliberately NOT done here (real follow-ups)

These are actually blocking meaningful *scaling* benchmarks but are out
of scope for the "can we measure at all?" pass:

- **Multi-partition Kafka topics**. Today all three topics are
  auto-created with 1 partition, so consumer-group scaling is capped at
  one consumer per group regardless of `Balancer: &kafka.Hash{}`. Fix:
  explicitly create topics with `partitions=8` (or so) in a bootstrap
  step in `docker-compose.yml`, and run multiple matching-service /
  pricing-service instances.
- **Env-var config**. Every host is still hardcoded (`localhost:9092`,
  `localhost:6379`, etc.). Fine for a single-host benchmark; blocks
  running any service on a different box.
- **Graceful shutdown**. Ctrl+C on pricing/matching/assignment leaves
  Kafka mid-commit. During a benchmark this can leave duplicate work
  on restart.
- **Consumer "retry" that's actually a silent drop**. The comment
  claims Kafka will retry on offset; the code moves on without
  committing, which drops the message mid-session. Fix: proper DLQ or
  explicit retry with backoff.
- **Distinguish match-failed from publish-failed** in
  `matching-service`. Today `_ = handler(event)` swallows both.
- **Grafana dashboards**. Prometheus's own UI is fine for reading
  metrics, but a permanent dashboard would make comparing benchmark
  runs faster.

Each of these can be picked up next.
