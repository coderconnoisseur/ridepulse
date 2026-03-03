![Go](https://img.shields.io/badge/Go-00ADD8?style=for-the-badge&logo=go&logoColor=white)
![Apache Kafka](https://img.shields.io/badge/Apache%20Kafka-231F20?style=for-the-badge&logo=apachekafka&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-DC382D?style=for-the-badge&logo=redis&logoColor=white)
![Apache Zookeeper](https://img.shields.io/badge/Apache%20Zookeeper-000000?style=for-the-badge&logo=apache&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-2496ED?style=for-the-badge&logo=docker&logoColor=white)
![Prometheus](https://img.shields.io/badge/Prometheus-E6522C?style=for-the-badge&logo=prometheus&logoColor=white)

# 🚕 RidePulse

RidePulse is a high-throughput, event-driven ride-matching system designed to simulate real-world dispatch infrastructure under surge conditions.

**Core focus areas:**
- Low-latency geospatial matching
- High-concurrency locking
- Contention-aware system design
- Observability-driven performance tuning

---

## 🏗 Architecture Overview

RidePulse follows an event-driven microservices design where each component has a single, well-scoped responsibility:

| Component | Role |
|---|---|
| **Rider Simulator** | Emits ride requests at configurable RPS |
| **Driver Simulator** | Continuously updates live driver locations |
| **Kafka** | Decouples ingestion from processing |
| **Matching Service** | Performs proximity search and atomic driver locking |
| **Redis** | GEO indexing and distributed driver locking |
| **Prometheus** | Latency, contention, and success-rate monitoring |

---

## 🔄 Request Flow

```
RiderSimulator emits RideRequested
       │
       ▼
   Kafka Topic
       │
       ▼
Pricing Service
       |
       ▼
 Matching Service
       │
       ├── Redis GEO → fetch nearby drivers from DriverSimulator
       │
       ├── SETNX + TTL → attempt atomic lock per driver
       │
       └── on success → emit RideMatched
```

---

## ⚙️ Core Engineering Decisions

### 1. Geospatial Indexing with Redis GEO

Driver locations are stored and queried using Redis's native GEO commands, enabling sub-millisecond radius-based lookups without a dedicated spatial database. This keeps the hot path lean and the driver discovery step real-time.

### 2. Atomic Driver Locking

```redis
SETNX driver:lock:<id> <ride_id> EX 3
```

Each driver is locked atomically before assignment. The TTL guarantees that locks are automatically released on service failure or timeout, making the system safe under high concurrency without explicit unlock logic in the happy path.

### 3. Progressive Parallel Locking

Rather than attempting all candidate drivers simultaneously (causing thundering herd issues), RidePulse:
- Shuffles the driver list to avoid hotspots
- Attempts locks in bounded parallel batches
- Uses context-based deadlines to prevent lock storms
- Sheds tail latency under surge by failing fast on overloaded paths

### 4. Worker Pool with Bounded Concurrency

A CPU-aware worker pool sits between Kafka consumption and matching logic. This provides natural backpressure — if matching falls behind, the pool queue fills rather than spawning unbounded goroutines, preventing memory blowup under load spikes.

### 5. Observability-Driven Tuning

Every performance decision in RidePulse was informed by Prometheus metrics rather than assumptions. The instrumentation tracks:
- Match latency (p50, p95, p99)
- Lock conflict rate
- Redis query latency
- Overall match success rate

This makes it straightforward to detect regressions, tune batch sizes, and validate changes under simulated load.

---

## 🚀 Performance Characteristics

Benchmarked under local load simulation:

- **Throughput:** 300–500 RPS sustained
- **Match latency:** Sub-second average
- **Lock contention:** Observable and measurable under hotspot traffic patterns
- **Overload behavior:** Deadline-based load shedding kicks in gracefully

---

## 🧰 Tech Stack

- **Golang** — Concurrency primitives, Goroutines, Contexts
- **Kafka** — Event streaming and ingestion decoupling
- **Redis** — GEO indexing and atomic distributed locking
- **PostgreSQL** — Extensible persistence layer
- **Docker** — Service orchestration
- **Prometheus** — Metrics and observability

---

## 🏃 Running RidePulse

### Prerequisites

- Docker and Docker Compose installed
- Go 1.21+

### 1. Start Infrastructure

From the project root, bring up Kafka and Redis:

```bash
docker-compose up -d
```

Verify that:
- Kafka is reachable at `localhost:9092`
- Redis is reachable at `localhost:6379`

### 2. Start the Matching Service

```bash
cd services/matching-service
go run cmd/matching-service/main.go
```

Prometheus metrics are exposed at `http://localhost:2112/metrics`.

### 3. Start the Driver Simulator

```bash
cd services/driver-simulator
go run cmd/driver-simulator/main.go
```

This continuously pushes driver location updates into Redis GEO.

### 4. Start the Rider Simulator

```bash
cd services/rider-simulator
go run cmd/rider-simulator/main.go
```

Generates ride requests at a configurable RPS rate.

### 5. Monitor Metrics

Scrape metrics directly or connect a Prometheus + Grafana stack. Key metrics to watch:

| Metric | Description |
|---|---|
| `matching_latency_seconds` | End-to-end match duration |
| `driver_lock_conflict_total` | Lock contention counter |
| `matching_success_total` | Successful match counter |
