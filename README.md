# RidePulse

Event-driven ride matching in Go: Kafka between every stage, Redis GEO for nearby-driver search, an atomic Redis claim for driver assignment, and Prometheus metrics end to end, load-tested with an automated RPS ladder.

[![CI](https://github.com/coderconnoisseur/ridepulse/actions/workflows/ci.yml/badge.svg)](https://github.com/coderconnoisseur/ridepulse/actions/workflows/ci.yml)
![Go 1.25](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Kafka](https://img.shields.io/badge/Kafka-231F20?logo=apachekafka&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-DC382D?logo=redis&logoColor=white)
![Prometheus](https://img.shields.io/badge/Prometheus-E6522C?logo=prometheus&logoColor=white)
![Docker Compose](https://img.shields.io/badge/Docker%20Compose-2496ED?logo=docker&logoColor=white)

## Results at a glance

From the committed benchmark run ([`benchmark-results.csv`](tools/benchmark/benchmark-results.csv)): one run on an 8-core Windows machine that also hosted Docker, Kafka, Redis and the load generator, with single-partition topics, 1,000 drivers and 5 s trips.

- **Accepts about 1,500 RPS:** the gateway held every target up to 1,200 RPS and accepted 1,543 RPS at a 1,600 target, with no publish errors in any measurement window.
- **Every ride gets a result:** all 211,750 requests ended matched or unmatched. None reached a dead-letter topic and no backlog remained.
- **Fast under load:** end-to-end p95 was 259 ms at 800 RPS, and about 1 s at the top step.
- **Matched rides are limited by driver supply:** about 160/sec with these settings. With 1 s trips, a separate run reached about 450/sec.

**Core focus areas:**
- Low-latency geospatial matching
- Atomic driver assignment with no locks or collisions
- Root-cause diagnosis of behaviour under load, from measurements
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
        BUSY[("driver:assigned:{id}<br/>10 min safety TTL")]
        RIDE[("ride:driver:{rideID}<br/>1 h idempotency record")]
    end
    MS -->|"one atomic claim: nearest free driver within 50 km"| GEO
    MS -->|"same script: set busy, ZREM"| BUSY
    MS -->|"same script: record ride"| RIDE
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
| **Matching Service** | Consumes `ride.priced`, atomically claims the nearest free driver (idempotent per ride), publishes `ride.matched`; if no driver is free, publishes `ride.unmatched` with reason `no_drivers`; infrastructure failures retried, then dead-lettered to `ride.priced.dlq` |
| **Assignment Service** | Consumes `ride.matched` and `ride.unmatched`, records each ride's outcome once (duplicates ignored), exposes end-to-end latency metrics and `GET /rides/<id>`; dead-letters to `ride.matched.dlq` / `ride.unmatched.dlq` |
| **Driver Simulator** | Continuously updates live driver locations in Redis GEO; keeps assigned drivers out of the GEO set and releases them after `DRIVER_TRIP_DURATION` |
| **Event contracts** (`pkg/events`) | Shared snake_case JSON types for `ride.requested`, `ride.priced`, `ride.matched` and `ride.unmatched`; every service imports them instead of keeping its own copy |
| **Shared runtime** (`pkg/kafkaconsumer`, `pkg/health`) | At-least-once consumer runner with ordered commits and DLQs; liveness and readiness handlers |
| **Kafka** | Decouples every stage of the pipeline |
| **Redis** | GEO indexing and driver assignment state, with one Lua script performing each claim atomically |
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
    Match->>Redis: claim script: nearest free driver within 50 km
    Note over Match,Redis: one atomic step, so rides cannot collide
    alt a free driver is in range
        Redis-->>Match: driver (driver:assigned and ride:driver set, removed from GEO)
        Match->>Kafka: ride.matched
        Kafka->>Assign: ride.matched
        Assign->>Assign: record the matched outcome once
    else no free driver in range
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

### 2. Atomic driver claim

One Lua script finds the nearest free driver within 50 km, marks them busy, records the ride and removes them from the GEO set in a single step. Redis runs scripts one at a time, so two rides can't collide over a driver, and each ride costs one Redis call.

This replaced a design of per-driver locks, parallel batches and phantom-lock release, after measurements showed it was the bottleneck: about 298,000 lock attempts for 5,905 matches in one run. It needs a single Redis instance, not Redis Cluster.

Full write-up: [matching-under-load.md](docs/matching-under-load.md).

### 3. Kafka batch window above the broker round trip

`kafka-go` closes batches on a timer and sends them one at a time per partition. A window shorter than a broker round trip builds a queue that grows under load, which caused publish timeouts, duplicates and dead letters. The window is now 25 ms with a 2 s publish budget. The cost is higher latency at low load: median end-to-end latency at 50 RPS rose from 40 ms to 91 ms.

### 4. At-least-once consumers with ordered commits and DLQs

Pricing, matching and assignment share one runner (`pkg/kafkaconsumer`). An offset is committed only after its message, and every earlier message of the partition, was handled or dead-lettered, so a crash redelivers instead of losing work. Failed handlers are retried with backoff, then written to `<topic>.dlq` with `x-error` / `x-original-*` headers; a handler panic is dead-lettered without retries instead of crashing the process; a consumer-group rewind starts a new tracker epoch so stale completions cannot corrupt commits; shutdown drains in-flight messages for up to 10 s.

Full write-up: [at-least-once-consumers.md](docs/at-least-once-consumers.md).

### 5. Driver state model and idempotent matching

A matched driver is now genuinely busy: matching atomically sets `driver:assigned:<id>`, writes `ride:driver:<rideID>` and removes the driver from the GEO set; the driver simulator ends the trip after `DRIVER_TRIP_DURATION` (default `5s`). Because the claim first checks `ride:driver:<rideID>`, a retry or Kafka redelivery returns the same driver instead of assigning a second one.

Full write-up: [driver-state-model.md](docs/driver-state-model.md).

### 6. Worker Pool with Bounded Concurrency

The shared consumer runner (`pkg/kafkaconsumer`) hands each fetched message to a fixed number of workers over an unbuffered channel. This provides natural backpressure — if handlers fall behind, fetching blocks rather than spawning unbounded goroutines or buffering messages, preventing memory blowup under load spikes.

### 7. Observability-Driven Tuning

Every stage exposes Prometheus metrics on its own port. `RequestedAt` is stamped at the api-gateway and propagated through every event so the terminal consumer can measure true end-to-end latency, not just per-stage latency.

Key exports:
- `gateway_request_duration_seconds`, `gateway_requests_total`, `gateway_publish_errors_total`
- `pricing_processing_duration_seconds`, `pricing_events_published_total`
- `matching_latency_seconds`, `matching_success_total`, `matching_failures_total`, `match_unmatched_published_total{reason}`
- `driver_claim_latency_seconds`
- `end_to_end_latency_seconds`, `assignments_total` (each ride counted once), `assignment_duplicates_total`
- `unmatched_total{reason}`, `unmatched_duplicates_total`, `unmatched_end_to_end_latency_seconds`
- `pricing_retries_total`, `matching_retries_total`, `pricing_dlq_messages_total`, `matching_dlq_messages_total`, `assignment_dlq_messages_total`, `match_idempotent_replays_total`

---

## Benchmarks

The [`tools/benchmark`](tools/benchmark) runner walks a configurable RPS ladder against a running cluster, samples Prometheus at each step, and writes one CSV row per step.


The latest run is committed here:

📊 **[tools/benchmark/benchmark-results.csv](tools/benchmark/benchmark-results.csv)**

| Target RPS | Gateway intake | Matched/sec | Unmatched/sec | E2E p50 | E2E p95 | DLQ/sec |
|---|---|---|---|---|---|---|
| 50 | 50 | 50 | 0 | 91 ms | 191 ms | 0 |
| 100 | 100 | 100 | 0 | 77 ms | 144 ms | 0 |
| 200 | 200 | 158 | 42 | 86 ms | 184 ms | 0 |
| 300 | 300 | 160 | 140 | 82 ms | 178 ms | 0 |
| 500 | 500 | 160 | 340 | 83 ms | 181 ms | 0 |
| 800 | 800 | 160 | 636 | 101 ms | 259 ms | 0 |
| 1,200 | 1,199 | 160 | 1,039 | 232 ms | 612 ms | 0 |
| 1,600 | 1,543 | 160 | 1,360 | 516 ms | 1,037 ms | 0 |

End-to-end latency covers matched rides. Matched rides level off because each driver is busy for a 5 s trip, so unmatched rides above that rate are expected. This is a single run on a shared machine, so treat the numbers as indicative. [matching-under-load.md](docs/matching-under-load.md) explains how the first live run's collapse at 800 RPS was diagnosed and fixed.

See [rps-benchmarking-setup.md](docs/rps-benchmarking-setup.md) for the runbook, ladder shape, PromQL queries, and interpretation notes.

---

## Known limitations and next steps

- **Limits at the top of the ladder aren't isolated.** Gateway p95 rises and intake falls short of target near 1,500 RPS. Everything shares one machine, so it may be the machine rather than the code.
- **Higher latency at low load.** The 25 ms batch window adds time at each Kafka hop, so median end-to-end latency is about 80-90 ms even when idle.
- **A single Redis instance is required.** The claim script builds key names at runtime, so it won't run on Redis Cluster as written.
- **A brief startup burst of claim timeouts.** While the Redis connection pool warms up, some claims can hit the 500 ms timeout. They are retried safely.
- **Single-partition Kafka topics.** One partition per topic caps consumer parallelism per service. Next: create topics with more partitions, partition by ride ID and run multiple consumers per stage. Until then the rewind handling below is not exercised.
- **Rewind detection is partial.** The offset tracker detects a rewind only when a re-fetched offset is at or below the last offset it tracked. A partition re-assigned starting *above* that offset is not detected, so the committed offset can move backwards and messages are redelivered. That stays within at-least-once, and handlers absorb duplicates.
- **Two readers, two consumer groups.** assignment-service reads `ride.matched` in group `assignment-service` and `ride.unmatched` in group `assignment-service-unmatched`. A shared group would make the leader's metadata request for both topics fail as a whole while `ride.unmatched` does not exist yet (it is created lazily on the first business failure), stalling the success path too; with separate groups a missing `ride.unmatched` topic only delays the unmatched reader, and rebalances are independent.
- **In-memory assignment store.** The assignment service keeps ride outcomes (matched and unmatched) in maps for benchmark verification; nothing is persisted, so after a restart a redelivered event is counted as new.
- **Test coverage is unit-level.** Unit tests cover the at-least-once consumer runner (ordered commits, retries, DLQ, drain, rewind epochs, panic recovery), the driver claim script (nearest-first, replay, concurrent claims) against an in-process Redis (miniredis), the shared event contracts (JSON round trips), the gateway's coordinate validation and the health probes. No automated test uses real Kafka or Redis. The matched and unmatched paths, dead-lettering, duplicate handling, driver release and restart recovery were checked by hand against a live stack, but consumer-group rewinds weren't. Next: integration tests against containerized Kafka and Redis.
- **Readiness probes are shallow.** `/ready` checks that a broker accepts a TCP connection (and Redis answers `PING`). It does not verify that the topics exist or that consumer groups are assigned partitions.

---

## Documentation

New here? Start with **[How RidePulse works, in simple terms](docs/how-it-works.md)**.

Deep-dive engineering notes on the correctness and reliability changes:

- 📄 [Finding what limited RidePulse under load](docs/matching-under-load.md)
- 📄 [At-least-once consumers](docs/at-least-once-consumers.md)
- 📄 [Driver state model](docs/driver-state-model.md)
- 📄 [RPS benchmarking setup](docs/rps-benchmarking-setup.md)

Earlier locking work, since replaced: [phantom-lock fix](docs/phantom-lock-fix.md), [deadline fix](docs/deadline-fix.md), [shuffle fix](docs/shuffle-nearest-first-fix.md).

---

## Tech Stack

- **Go 1.25** — Concurrency primitives, goroutines, contexts; Gin for the HTTP gateway
- **Kafka** — Event streaming and ingestion decoupling
- **Redis** — GEO indexing and atomic driver assignment with a Lua script
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
| `REDIS_POOL_SIZE` | `MATCH_WORKERS` | matching | Redis connection pool size; positive integer. Each worker uses one connection at a time. |
| `PRICING_WORKERS` | `runtime.NumCPU() * 25` | pricing | Pricing worker-pool size; positive integer. Each worker blocks on a Kafka publish, so this caps pipeline throughput. |
| `MATCH_DEADLINE` | `500ms` | matching | Timeout for one driver claim (Go duration, positive). A claim that times out is retried. |
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

# Driver claim latency
histogram_quantile(0.95, sum by (le) (rate(driver_claim_latency_seconds_bucket[1m])))

# Rides that found no driver
sum by (reason) (rate(unmatched_total[1m]))
```

Verify pipeline closure by querying assignment-service directly:

```bash
curl http://localhost:2114/count            # total matched rides observed
curl http://localhost:2114/assignments/<id> # look up a matched ride (404 if unmatched)
curl http://localhost:2114/rides/<id>       # outcome of any ride: matched or unmatched
```

`GET /rides/<id>` returns `{"ride_id","status":"matched","driver_id","matched_at","latency_ms"}` or `{"ride_id","status":"unmatched","reason","failed_at"}` (`reason` is `no_drivers`), and 404 for a ride with no outcome yet.

### 5. Run the checks CI runs

Each service is its own Go module (tied together by `services/go.work` for local development). CI runs `go vet`, `go build`, and `go test` in every module; the unit tests need no Kafka or Redis:

```bash
cd services/matching-service
go vet ./... && go build ./... && go test ./...
```

## License

[MIT](LICENSE)
