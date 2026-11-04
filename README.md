# RidePulse

Event-driven ride matching in Go: Kafka between every stage, Redis GEO for nearby-driver search, atomic Redis locks for driver assignment, and Prometheus metrics end to end, load-tested with an automated RPS ladder.

[![CI](https://github.com/coderconnoisseur/ridepulse/actions/workflows/ci.yml/badge.svg)](https://github.com/coderconnoisseur/ridepulse/actions/workflows/ci.yml)
![Go 1.25](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Kafka](https://img.shields.io/badge/Kafka-231F20?logo=apachekafka&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-DC382D?logo=redis&logoColor=white)
![Prometheus](https://img.shields.io/badge/Prometheus-E6522C?logo=prometheus&logoColor=white)
![Docker Compose](https://img.shields.io/badge/Docker%20Compose-2496ED?logo=docker&logoColor=white)

## Results at a glance

From the committed benchmark run ([`benchmark-results.csv`](tools/benchmark/benchmark-results.csv)): one run on an 8-core Windows machine that also hosted Docker, Kafka, Redis and the load generator, with single-partition topics, 1,000 drivers and 5 s trips.

- **Healthy up to 100 RPS:** 100 % of rides matched at 50 and 100 RPS, end-to-end p95 about 170 ms.
- **Peak throughput:** about 134 matched rides/sec.
- **Past that, contention limits matching:** at a 300 RPS target, 46 % of rides matched and the rest mostly got a recorded unmatched result. End-to-end p95 stayed near 1 s, with no Kafka backlog left after the run.
- **Where it breaks:** at an 800 RPS target, gateway intake collapsed to about 45 RPS, with publish errors and a 2 s gateway p95. The cause isn't diagnosed (see [Known limitations](#known-limitations-and-next-steps)).

**Core focus areas:**
- Low-latency geospatial matching
- High-concurrency locking with explicit phantom-lock release
- Contention-aware batching and progressive deadline hierarchy
- Observability-driven performance tuning end-to-end

---

## Architecture Overview

RidePulse follows an event-driven microservices design where each component has a single, well-scoped responsibility:

```mermaid
flowchart LR
    RS["rider-simulator<br/>load generator"] -->|"HTTP POST /rides"| GW["api-gateway"]
    GW -->|"produce"| T1[["ride.requested"]]
    T1 -->|"group pricing-service"| PR["pricing-service"]
    PR -->|"produce"| T2[["ride.priced"]]
    T2 -->|"group matching-service"| MS["matching-service"]
    MS -->|"produce, on match"| T3[["ride.matched"]]
    MS -->|"produce, on failure"| T4[["ride.unmatched"]]
    T3 -->|"group assignment-service"| AS["assignment-service"]
    T4 -->|"group assignment-service-unmatched"| AS

    PR -.->|"after retries"| D1[("ride.requested.dlq")]
    MS -.->|"after retries"| D2[("ride.priced.dlq")]
    AS -.->|"after retries"| D3[("ride.matched.dlq")]
    AS -.->|"after retries"| D4[("ride.unmatched.dlq")]

    subgraph Redis
        GEO[("drivers:locations<br/>GEO index")]
        LOCK[("driver:lock:{id}<br/>SETNX, 3 s TTL")]
        BUSY[("driver:assigned:{id}<br/>10 min safety TTL")]
        RIDE[("ride:driver:{rideID}<br/>1 h idempotency record")]
    end
    MS -->|"GEORADIUS 50 km"| GEO
    MS -->|"lock candidates"| LOCK
    MS -->|"assign: set busy, set ride, ZREM, DEL lock"| BUSY
    MS -->|"assign"| RIDE
    DS["driver-simulator"] -->|"GEOADD while not busy"| GEO
    DS -->|"release after DRIVER_TRIP_DURATION"| BUSY

    PROM[("Prometheus")] -.->|"scrape /metrics"| GW
    PROM -.-> PR
    PROM -.-> MS
    PROM -.-> AS
```

Every arrow above is a real code path. Each consumer runs in its own consumer group, and a message that still fails after retries is dead-lettered to `<topic>.dlq` with `x-error` and `x-original-*` headers. The driver simulator is not an event consumer: it writes locations straight to Redis and deletes `driver:assigned:{id}` when a trip ends.

The `ride.*` payloads share one contract, [`pkg/events`](pkg/events/events.go): snake_case JSON and a `v` version field (currently `1`).

| Component | Role |
|---|---|
| **Rider Simulator** | Generates ride requests at configurable RPS against the HTTP gateway |
| **API Gateway** | Accepts `POST /rides`, publishes `ride.requested` to Kafka |
| **Pricing Service** | Consumes `ride.requested`, computes fare, publishes `ride.priced`; failures retried, then dead-lettered to `ride.requested.dlq` |
| **Matching Service** | Consumes `ride.priced`, runs geospatial search + atomic driver locking, assigns the driver (idempotent per ride), publishes `ride.matched`; business failures (`no_drivers`, `match_deadline`) publish `ride.unmatched`; infrastructure failures retried, then dead-lettered to `ride.priced.dlq` |
| **Assignment Service** | Consumes `ride.matched` and `ride.unmatched`, records each ride's outcome once (duplicates ignored), exposes end-to-end latency metrics and `GET /rides/<id>`; dead-letters to `ride.matched.dlq` / `ride.unmatched.dlq` |
| **Driver Simulator** | Continuously updates live driver locations in Redis GEO; keeps assigned drivers out of the GEO set and releases them after `DRIVER_TRIP_DURATION` |
| **Event contracts** (`pkg/events`) | Shared snake_case JSON types for `ride.requested`, `ride.priced`, `ride.matched` and `ride.unmatched`; every service imports them instead of keeping its own copy |
| **Shared runtime** (`pkg/kafkaconsumer`, `pkg/health`) | At-least-once consumer runner with ordered commits and DLQs; liveness and readiness handlers |
| **Kafka** | Decouples every stage of the pipeline |
| **Redis** | GEO indexing, distributed driver locking (Lua safe-unlock) and driver assignment state |
| **Prometheus** | Latency, contention, and success-rate monitoring at every stage |

---

## Request Flow

One ride, from `POST /rides` to a lookup of its outcome:

```mermaid
sequenceDiagram
    autonumber
    participant Rider as rider-simulator
    participant GW as api-gateway
    participant Kafka
    participant Pricing as pricing-service
    participant Match as matching-service
    participant Redis
    participant Assign as assignment-service

    Rider->>GW: POST /rides {pickup, drop}
    GW->>GW: validate lat and lng, new ride_id, requested_at
    GW->>Kafka: ride.requested, rider gets 503 if this fails
    GW-->>Rider: 201 {ride_id, status REQUESTED}
    Kafka->>Pricing: ride.requested
    Pricing->>Kafka: ride.priced {price, currency, surge}
    Kafka->>Match: ride.priced
    Match->>Redis: GEORADIUS drivers:locations within 50 km
    Note over Match,Redis: tier-preserving shuffle, batches of 5 SETNX driver:lock
    alt a free driver locks and assigns
        Match->>Redis: set driver:assigned, set ride:driver, ZREM from GEO, DEL lock
        Match->>Kafka: ride.matched
        Kafka->>Assign: ride.matched
        Assign->>Assign: record the matched outcome once
    else no driver, or match deadline passed
        Match->>Kafka: ride.unmatched {reason}
        Kafka->>Assign: ride.unmatched
        Assign->>Assign: record the unmatched outcome once
    end
    Rider->>Assign: GET /rides/{ride_id}
    Assign-->>Rider: matched or unmatched outcome, 404 until recorded
```

Latency is measured from `requested_at` (stamped at the gateway) to the terminal consumer, so the same number covers every stage.

---

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

Each match has its own budget (`MATCH_DEADLINE`, default 500 ms; the write-up below was done at 800 ms); each batch of 5 parallel `SETNX` attempts has a 200 ms budget nested inside it; Kafka publish gets its own independent 300 ms budget so a slow match can't starve the publish RTT.

Full write-up: [deadline-fix.md](docs/deadline-fix.md).

### 5. At-least-once consumers with ordered commits and DLQs

Pricing, matching and assignment share one runner (`pkg/kafkaconsumer`). An offset is committed only after its message, and every earlier message of the partition, was handled or dead-lettered, so a crash redelivers instead of losing work. Failed handlers are retried with backoff, then written to `<topic>.dlq` with `x-error` / `x-original-*` headers; a handler panic is dead-lettered without retries instead of crashing the process; a consumer-group rewind starts a new tracker epoch so stale completions cannot corrupt commits; shutdown drains in-flight messages for up to 10 s.

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
- `matching_latency_seconds`, `matching_success_total`, `matching_failures_total`, `match_unmatched_published_total{reason}`
- `driver_lock_conflict_total`, `driver_lock_released_phantom_total`
- `end_to_end_latency_seconds`, `assignments_total` (each ride counted once), `assignment_duplicates_total`
- `unmatched_total{reason}`, `unmatched_duplicates_total`, `unmatched_end_to_end_latency_seconds`
- `pricing_retries_total`, `matching_retries_total`, `pricing_dlq_messages_total`, `matching_dlq_messages_total`, `assignment_dlq_messages_total`, `match_idempotent_replays_total`

---

## Benchmarks

The [`tools/benchmark`](tools/benchmark) runner walks a configurable RPS ladder against a running cluster, samples Prometheus at each step, and writes one CSV row per step.


The latest run is committed here:

📊 **[tools/benchmark/benchmark-results.csv](tools/benchmark/benchmark-results.csv)**

| Target RPS | Gateway intake | Matched/sec | Matched % | E2E p50 | E2E p95 | Match p95 |
|---|---|---|---|---|---|---|
| 50 | 50 | 50 | 100 % | 40 ms | 172 ms | 74 ms |
| 100 | 100 | 100 | 100 % | 41 ms | 156 ms | 79 ms |
| 200 | 154 | 134 | 87 % | 415 ms | 790 ms | 970 ms |
| 300 | 281 | 130 | 46 % | 484 ms | 998 ms | 858 ms |
| 500 | 221 | 121 | 55 % | 675 ms | 1,231 ms | 931 ms |
| 800 | 45 | 23 | 50 % | 2,311 ms | 4,615 ms | 947 ms |

"Matched %" is matched rides divided by gateway intake. Rides that aren't matched mostly get a recorded `ride.unmatched` result, so it isn't a loss rate. This is a single run on a shared machine, so treat the numbers as indicative.

**What the live run found.** The first run showed about 21,700 rides queued at pricing against about 200 at matching: pricing's 8 workers each block on a Kafka write, capping the pipeline well below the matcher. Moving to 200 workers (`PRICING_WORKERS`) removed the backlog. At 100 RPS, end-to-end p95 fell from 1,581 ms to 156 ms; at a 300 RPS target, from the 10 s histogram cap to about 1 s. Pricing's timeout dead-letters fell from 184 to 0.

See [rps-benchmarking-setup.md](docs/rps-benchmarking-setup.md) for the runbook, ladder shape, PromQL queries, and interpretation notes.

---

## Known limitations and next steps

- **Lock contention caps matching near 134 rides/sec.** That's below the 200/sec that 1,000 drivers on 5 s trips could supply. Next: fail fast when the first batch is fully contended, and widen the candidate pool.
- **`no_drivers` is overstated under load.** The reason is also used when all 20 nearest drivers were merely locked by other rides.
- **Match p95 exceeds the 500 ms deadline under load** (about 900 ms). The likely cause is that Redis calls don't enforce context timeouts (`ContextTimeoutEnabled` is off), but this hasn't been confirmed.
- **Rides can still reach the dead-letter topic under overload.** 213 of about 42,000 rides in the run ended in `ride.priced.dlq`. In the first run, those dead letters were all `context deadline exceeded`. A timed-out publish can also succeed anyway, which causes the duplicates the consumers then ignore (about 1,900 in the run).
- **The 800 RPS collapse is undiagnosed.** Gateway intake fell to about 45 RPS with publish errors. Everything shares one machine, so resource exhaustion is possible, but it hasn't been isolated.
- **Single-partition Kafka topics.** One partition per topic caps consumer parallelism per service. Next: create topics with more partitions, partition by ride ID and run multiple consumers per stage. Until then the rewind handling below is not exercised.
- **Rewind detection is partial.** The offset tracker detects a rewind only when a re-fetched offset is at or below the last offset it tracked. A partition re-assigned starting *above* that offset is not detected, so the committed offset can move backwards and messages are redelivered. That stays within at-least-once, and handlers absorb duplicates.
- **Two readers, two consumer groups.** assignment-service reads `ride.matched` in group `assignment-service` and `ride.unmatched` in group `assignment-service-unmatched`. A shared group would make the leader's metadata request for both topics fail as a whole while `ride.unmatched` does not exist yet (it is created lazily on the first business failure), stalling the success path too; with separate groups a missing `ride.unmatched` topic only delays the unmatched reader, and rebalances are independent.
- **In-memory assignment store.** The assignment service keeps ride outcomes (matched and unmatched) in maps for benchmark verification; nothing is persisted, so after a restart a redelivered event is counted as new.
- **Test coverage is unit-level.** Unit tests cover the tier-preserving shuffle, the at-least-once consumer runner (ordered commits, retries, DLQ, drain, rewind epochs, panic recovery), the Redis Lua scripts (locking, assignment, release) against an in-process Redis (miniredis), the shared event contracts (JSON round trips), the gateway's coordinate validation and the health probes. No automated test uses real Kafka or Redis. The matched and unmatched paths, dead-lettering, duplicate handling, driver release and restart recovery were checked by hand against a live stack, but consumer-group rewinds weren't. Next: integration tests against containerized Kafka and Redis.
- **Readiness probes are shallow.** `/ready` checks that a broker accepts a TCP connection (and Redis answers `PING`). It does not verify that the topics exist or that consumer groups are assigned partitions.

---

## Documentation

New here? Start with **[How RidePulse works, in simple terms](docs/how-it-works.md)**.

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

**Health endpoints.** Each service answers two probes on the same port as its HTTP or metrics server:

| Service | Port | `GET /health` (liveness) | `GET /ready` (readiness) |
|---|---|---|---|
| api-gateway | `HTTP_ADDR` (`:8080`) | 200 while the process serves HTTP | Kafka |
| pricing-service | `METRICS_ADDR` (`:2115`) | 200 while the process serves HTTP | Kafka |
| matching-service | `METRICS_ADDR` (`:2112`) | 200 while the process serves HTTP | Kafka and Redis `PING` |
| assignment-service | `HTTP_ADDR` (`:2114`) | 200 while the process serves HTTP | Kafka |

`/ready` returns `200 {"status":"ready"}` when every check passes, or `503` with the failing checks listed under `failing`. The Kafka check succeeds when any one broker in `KAFKA_BROKERS` accepts a TCP connection. All checks run concurrently under a 1 s budget, and none creates topics or keys.

**Configuration.** Every service reads its endpoints from environment variables; unset or blank values fall back to the defaults below, so the commands above work unchanged on one host. `KAFKA_BROKERS` is a comma-separated list. Invalid values make the services exit at startup, except rider-simulator, which falls back to the default for a non-numeric or non-positive `RIDER_RPS` / `RIDER_CONCURRENCY`; double-check these when running the ladder.

| Variable | Default | Used by | Meaning |
|---|---|---|---|
| `KAFKA_BROKERS` | `localhost:9092` | api-gateway, pricing, matching, assignment | Comma-separated broker list. |
| `REDIS_ADDR` | `localhost:6379` | matching, driver-simulator | Redis address. |
| `HTTP_ADDR` | `:8080` (api-gateway), `:2114` (assignment) | api-gateway, assignment | HTTP listen address (also serves `/metrics`). |
| `METRICS_ADDR` | `:2115` (pricing), `:2112` (matching) | pricing, matching | Metrics listen address. |
| `MATCH_WORKERS` | `runtime.NumCPU() * 25` | matching | Matcher worker-pool size; positive integer. |
| `REDIS_POOL_SIZE` | `MATCH_WORKERS * 5` | matching | Redis connection pool size; positive integer. Each worker runs up to 5 parallel `SETNX` attempts, while the go-redis default is only 10 x GOMAXPROCS. |
| `PRICING_WORKERS` | `runtime.NumCPU() * 25` | pricing | Pricing worker-pool size; positive integer. Each worker blocks on a Kafka publish, so this caps pipeline throughput. |
| `MATCH_DEADLINE` | `500ms` | matching | Total per-match budget (Go duration); must exceed the 200 ms batch deadline. |
| `DRIVER_COUNT` | `1000` | driver-simulator | Number of simulated drivers. |
| `DRIVER_TRIP_DURATION` | `5s` | driver-simulator | How long an assigned driver stays busy (Go duration). |
| `RIDER_URL` | `http://localhost:8080/rides` | rider-simulator | Gateway endpoint to POST rides to. |
| `RIDER_RPS` | `500` | rider-simulator | Target requests per second. |
| `RIDER_CONCURRENCY` | `200` | rider-simulator | Max in-flight requests; sends beyond it are counted as dropped. |

The committed benchmark uses these defaults.

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

# Rides that found no driver, by reason (no_drivers | match_deadline)
sum by (reason) (rate(unmatched_total[1m]))
```

Verify pipeline closure by querying assignment-service directly:

```bash
curl http://localhost:2114/count            # total matched rides observed
curl http://localhost:2114/assignments/<id> # look up a matched ride (404 if unmatched)
curl http://localhost:2114/rides/<id>       # outcome of any ride: matched or unmatched
```

`GET /rides/<id>` returns `{"ride_id","status":"matched","driver_id","matched_at","latency_ms"}` or `{"ride_id","status":"unmatched","reason","failed_at"}` (`reason` is `no_drivers` or `match_deadline`), and 404 for a ride with no outcome yet.

### 5. Run the checks CI runs

Each service is its own Go module (tied together by `services/go.work` for local development). CI runs `go vet`, `go build`, and `go test` in every module; the unit tests need no Kafka or Redis:

```bash
cd services/matching-service
go vet ./... && go build ./... && go test ./...
```

## License

[MIT](LICENSE)
