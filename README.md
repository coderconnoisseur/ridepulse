# RidePulse

Event-driven ride matching in Go: Kafka between every stage, Redis GEO for nearby-driver search, atomic Redis locks for driver assignment, and Prometheus metrics end to end, load-tested with an automated RPS ladder.

[![CI](https://github.com/coderconnoisseur/ridepulse/actions/workflows/ci.yml/badge.svg)](https://github.com/coderconnoisseur/ridepulse/actions/workflows/ci.yml)
![Go 1.25](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Kafka](https://img.shields.io/badge/Kafka-231F20?logo=apachekafka&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-DC382D?logo=redis&logoColor=white)
![Prometheus](https://img.shields.io/badge/Prometheus-E6522C?logo=prometheus&logoColor=white)
![Docker Compose](https://img.shields.io/badge/Docker%20Compose-2496ED?logo=docker&logoColor=white)

## Results at a glance

From the committed benchmark run ([`benchmark-results.csv`](tools/benchmark/benchmark-results.csv); Windows dev box, single-partition Kafka, single Redis). These numbers predate the at-least-once consumer and driver-state changes: drivers now stay busy for `DRIVER_TRIP_DURATION`, so the ladder must be re-run before results are comparable.

- **50 RPS:** 99.7 % of rides matched, end-to-end p50 34 ms / p95 94 ms (gateway to assignment).
- **Peak throughput:** the gateway took in ~400 RPS; the pipeline completed ~150 assignments/sec.
- **Matching stayed fast under load:** successful-match p95 was 33–84 ms at every step up to 800 RPS target.
- **Where it breaks:** success drops to 66 % at 200 RPS and below 50 % from 300 RPS, and end-to-end p95 hits the 10 s histogram cap. The primary bottleneck identified is matcher worker-pool starvation (see [Known limitations](#known-limitations-and-next-steps)).

**Core focus areas:**
- Low-latency geospatial matching
- High-concurrency locking with explicit phantom-lock release
- Contention-aware batching and progressive deadline hierarchy
- Observability-driven performance tuning end-to-end

---

## Architecture Overview

RidePulse follows an event-driven microservices design where each component has a single, well-scoped responsibility:

![RidePulse Architecture](./assets/Architecture.png)

*The diagram shows the core Kafka/Redis pipeline. The API gateway (in front of `ride.requested`) and the assignment service (consuming `ride.matched`) are shown in the table and in [Request Flow](#request-flow) below.*

| Component | Role |
|---|---|
| **Rider Simulator** | Generates ride requests at configurable RPS against the HTTP gateway |
| **API Gateway** | Accepts `POST /rides`, publishes `ride.requested` to Kafka |
| **Pricing Service** | Consumes `ride.requested`, computes fare, publishes `ride.priced`; failures retried, then dead-lettered to `ride.requested.dlq` |
| **Matching Service** | Consumes `ride.priced`, runs geospatial search + atomic driver locking, assigns the driver (idempotent per ride), publishes `ride.matched`; infrastructure failures retried, then dead-lettered to `ride.priced.dlq` |
| **Assignment Service** | Consumes `ride.matched`, records the assignment, exposes end-to-end latency metrics; dead-letters to `ride.matched.dlq` |
| **Driver Simulator** | Continuously updates live driver locations in Redis GEO; keeps assigned drivers out of the GEO set and releases them after `DRIVER_TRIP_DURATION` |
| **Kafka** | Decouples every stage of the pipeline |
| **Redis** | GEO indexing, distributed driver locking (Lua safe-unlock) and driver assignment state |
| **Prometheus** | Latency, contention, and success-rate monitoring at every stage |

---

## Request Flow

```
RiderSimulator ──HTTP POST /rides──▶ API Gateway
                                        │  (stamps RequestedAt)
                                        ▼
                                Kafka: ride.requested
                                        │
                                        ▼
                                 Pricing Service
                                        │
                                        ▼
                                 Kafka: ride.priced
                                        │
                                        ▼
                                 Matching Service
                                        │
                                        ├── Redis GEORADIUS → nearby drivers
                                        ├── tier-preserving shuffle
                                        ├── SETNX + TTL → atomic driver lock
                                        │   (batch losers explicitly released)
                                        ├── atomic assign: driver:assigned, ride:driver,
                                        │   remove from GEO (replays return same driver)
                                        ▼
                                 Kafka: ride.matched
                                        │
                                        ▼
                                Assignment Service
                                        │
                                        ▼
                        records assignment; end_to_end_latency
```

---

## Core Engineering Decisions

### 1. Geospatial Indexing with Redis GEO

Driver locations are stored and queried using Redis's native GEO commands, enabling sub-millisecond radius-based lookups without a dedicated spatial database. This keeps the hot path lean and the driver discovery step real-time.

### 2. Atomic Driver Locking with phantom-lock release

```
SETNX driver:lock:<id> <ride_id> EX 3
```

Each driver is locked atomically before assignment. Because the matcher races 5 goroutines per batch and any of them can `SETNX`-win, the batch race can produce **more than one successful lock per ride**. The extras are released via a Lua `GET`-then-`DEL` safe-unlock so they never sit in Redis as phantom locks blocking future rides. The `driver_lock_released_phantom_total` counter makes this visible.

Full write-up: [phantom-lock-fix.md](docs/phantom-lock-fix.md).

### 3. Tier-preserving batch shuffle

`GEORADIUS` returns drivers nearest-first — the signal we want to preserve. But under a demand hotspot, every ride's "top 5 nearest" is the same set, and pure nearest-first cascades into lock contention. Full random shuffle destroys the geospatial signal entirely.

The matcher shuffles **within** distance tiers of size 10, keeping tier order intact. Under spread load this recovers pure nearest-first; under hotspot load it spreads contention across the top-10 instead of pinning it to the top-5, without ever trying a farther driver before a nearer one.

Full write-up: [shuffle-nearest-first-fix.md](docs/shuffle-nearest-first-fix.md).

### 4. Progressive Parallel Locking with hierarchical deadlines

Each match has its own 800 ms budget; each batch of 5 parallel `SETNX` attempts has a 200 ms budget nested inside it; Kafka publish gets its own independent 300 ms budget so a slow match can't starve the publish RTT.

Full write-up: [deadline-fix.md](docs/deadline-fix.md).

### 5. At-least-once consumers with ordered commits and DLQs

Pricing, matching and assignment share one runner (`pkg/kafkaconsumer`). An offset is committed only after its message, and every earlier message of the partition, was handled or dead-lettered, so a crash redelivers instead of losing work. Failed handlers are retried with backoff, then written to `<topic>.dlq` with `x-error` / `x-original-*` headers; shutdown drains in-flight messages for up to 10 s.

Full write-up: [at-least-once-consumers.md](docs/at-least-once-consumers.md).

### 6. Driver state model and idempotent matching

A matched driver is now genuinely busy: matching atomically sets `driver:assigned:<id>`, writes `ride:driver:<rideID>` and removes the driver from the GEO set; the driver simulator ends the trip after `DRIVER_TRIP_DURATION` (default `5s`). Because `Match` first checks `ride:driver:<rideID>`, a retry or Kafka redelivery returns the same driver instead of assigning a second one.

Full write-up: [driver-state-model.md](docs/driver-state-model.md).

### 7. Worker Pool with Bounded Concurrency

The shared consumer runner (`pkg/kafkaconsumer`) hands each fetched message to a fixed number of workers over an unbuffered channel. This provides natural backpressure — if handlers fall behind, fetching blocks rather than spawning unbounded goroutines or buffering messages, preventing memory blowup under load spikes.

### 8. Observability-Driven Tuning

Every stage exposes Prometheus metrics on its own port. `RequestedAt` is stamped at the api-gateway and propagated through every event so the terminal consumer can measure true end-to-end latency, not just per-stage latency.

Key exports:
- `gateway_request_duration_seconds`, `gateway_requests_total`, `gateway_publish_errors_total`
- `pricing_processing_duration_seconds`, `pricing_events_published_total`
- `matching_latency_seconds`, `matching_success_total`, `matching_failures_total`
- `driver_lock_conflict_total`, `driver_lock_released_phantom_total`
- `end_to_end_latency_seconds`, `assignments_total`
- `pricing_retries_total`, `matching_retries_total`, `pricing_dlq_messages_total`, `matching_dlq_messages_total`, `assignment_dlq_messages_total`, `match_idempotent_replays_total`

---

## Benchmarks

The [`tools/benchmark`](tools/benchmark) runner walks a configurable RPS ladder against a running cluster, samples Prometheus at each step, and writes one CSV row per step.

> **Note:** the committed results below predate the at-least-once and driver-state changes. Drivers now stay busy for `DRIVER_TRIP_DURATION` after a match, so supply is finite and the ladder must be re-run before new numbers are comparable with this CSV.

The latest run is committed here:

📊 **[tools/benchmark/benchmark-results.csv](tools/benchmark/benchmark-results.csv)**

Headline from that run (Windows dev box, single-partition Kafka, single Redis, ~8 cores):

| What | Value |
|---|---|
| Peak intake at gateway | ~400 RPS |
| Peak end-to-end assignment throughput | ~150 rides/sec |
| Peak "healthy" throughput (>95 % success) | ~50 RPS |
| Successful `matching_latency_seconds` p95 | stayed 33–84 ms across every step |

The primary bottleneck is matcher pool starvation from slow-fail matches: under contention, ~30 % of matches consume workers for the full 800 ms `matchDeadline`, blowing past `NumCPU × 5` worker capacity. Fixing this (fail-fast on batch 1 saturation, multi-partition Kafka topics, larger pool) is the next set of unlocks.

See [rps-benchmarking-setup.md](docs/rps-benchmarking-setup.md) for the runbook, ladder shape, PromQL queries, and interpretation notes.

---

## Known limitations and next steps

- **Matcher pool starvation.** The pool is `runtime.NumCPU() * 5` workers and each match may hold one for up to the 800 ms `matchDeadline`. Under contention, slow-fail matches occupy workers until the deadline and the queue backs up. Next: fail fast when the first batch is fully contended instead of waiting out the deadline, then re-measure pool size.
- **Single-partition Kafka topics.** One partition per topic caps consumer parallelism per service. Next: partition by ride ID and run multiple consumers per stage.
- **Hardcoded endpoints.** Services connect to `localhost:9092` and `localhost:6379` directly; only the rider simulator and driver simulator (`REDIS_ADDR`) read their config from environment variables. Next: env-based config so services can run inside Compose.
- **In-memory assignment store.** The assignment service keeps results in a map for benchmark verification; nothing is persisted.
- **Test coverage is unit-level.** Unit tests cover the tier-preserving shuffle, the at-least-once consumer runner (ordered commits, retries, DLQ, drain) and the Redis Lua scripts (locking, assignment, release) against an in-process Redis (miniredis). Nothing yet exercises real Kafka/Redis end to end. Next: integration tests against containerized Kafka and Redis.

---

## Documentation

Deep-dive engineering notes on the correctness and reliability changes:

- 📄 [Phantom-lock race fix](docs/phantom-lock-fix.md)
- 📄 [Deadline hierarchy fix](docs/deadline-fix.md)
- 📄 [Shuffle vs nearest-first fix](docs/shuffle-nearest-first-fix.md)
- 📄 [At-least-once consumers](docs/at-least-once-consumers.md)
- 📄 [Driver state model](docs/driver-state-model.md)
- 📄 [RPS benchmarking setup](docs/rps-benchmarking-setup.md)

---

## Tech Stack

- **Go 1.25** — Concurrency primitives, goroutines, contexts; Gin for the HTTP gateway
- **Kafka** — Event streaming and ingestion decoupling
- **Redis** — GEO indexing and atomic distributed locking (with Lua safe-unlock)
- **Docker Compose** — Infrastructure orchestration
- **Prometheus** — Metrics and observability

---

## Running RidePulse

### Prerequisites

- Docker with Compose (`docker compose`, or the older `docker-compose`)
- Go 1.25.6+ (the version in each `go.mod`)

### 1. Start Infrastructure

From `deployment/`, bring up Kafka, Zookeeper, Redis, and Prometheus:

```bash
cd deployment
docker compose up -d
```

Verify:
- Kafka: `localhost:9092`
- Redis: `localhost:6379`
- Prometheus UI: http://localhost:9090

### 2. Start the services (one terminal each, from the repo root)

```bash
cd services/matching-service   && go run cmd/matching-service/main.go
cd services/pricing-service    && go run cmd/pricing-service/main.go
cd services/assignment-service && go run cmd/assignment-service/main.go
cd services/driver-simulator   && go run cmd/driver-simulator/main.go
cd services/api-gateway        && go run cmd/api-gateway/main.go
```

Prometheus scrape targets:

| Service | `/metrics` endpoint |
|---|---|
| api-gateway | `:8080/metrics` |
| pricing-service | `:2115/metrics` |
| matching-service | `:2112/metrics` |
| assignment-service | `:2114/metrics` |

Confirm all four are `up` at http://localhost:9090/targets.

### 3. Send load

**Automated ladder (recommended):**

```bash
cd tools/benchmark
go run cmd/benchmark/main.go
```

Walks `50:20,100:40,200:80,300:120,500:200,800:300` (RPS:concurrency) by default and writes [`benchmark-results.csv`](tools/benchmark/benchmark-results.csv). Configurable via `--ladder`, `--warmup`, `--window`, `--cooldown`, `--output`.

Driver supply is configurable on the driver simulator: `DRIVER_COUNT` (default `1000`), `DRIVER_TRIP_DURATION` (Go duration, default `5s`) and `REDIS_ADDR` (default `localhost:6379`).

**Manual, one RPS level:**

```bash
cd services/rider-simulator
RIDER_RPS=300 RIDER_CONCURRENCY=120 go run cmd/rider-simulator/main.go
```

### 4. Monitor

Prometheus at http://localhost:9090. Key queries:

```promql
# End-to-end assignment rate
rate(assignments_total[1m])

# End-to-end p95 latency
histogram_quantile(0.95, sum by (le) (rate(end_to_end_latency_seconds_bucket[1m])))

# Where matches are failing (contention vs deadline)
rate(driver_lock_conflict_total[1m])
rate(driver_lock_released_phantom_total[1m])
```

Verify pipeline closure by querying assignment-service directly:

```bash
curl http://localhost:2114/count            # total assignments observed
curl http://localhost:2114/assignments/<id> # look up a specific ride
```

### 5. Run the checks CI runs

Each service is its own Go module (tied together by `services/go.work` for local development). CI runs `go vet`, `go build`, and `go test` in every module; the unit tests need no Kafka or Redis:

```bash
cd services/matching-service
go vet ./... && go build ./... && go test ./...
```
