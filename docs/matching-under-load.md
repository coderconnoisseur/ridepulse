# Finding what limited RidePulse under load

The first live benchmark looked bad: at a target of 800 requests per second (RPS), gateway intake collapsed to 45 RPS, matching was slower than its own deadline, drivers collided constantly, and some rides ended in a dead-letter topic. Each symptom was investigated with measurements before anything changed, and each fix was tested alone.

Four separate causes turned up. One was in the load generator, not the system.

## How it was measured

Every experiment held 800 RPS for 45 s on a clean stack, sampling per-process CPU, TCP state, Redis command stats, Kafka lag and `kafka-go` writer stats. Everything ran on one 8-core Windows machine, with Kafka and Redis in Docker.

| Change, applied cumulatively | Rider errors | Gateway intake | `ride.priced` backlog at the end | Matching DLQ | Replays |
|---|---|---|---|---|---|
| Starting point | hundreds per second in bursts | unstable, often under 500 | 4,325 | 2,268 | about 8,500 |
| Reuse HTTP connections | 0 | about 800 | 4,973 | 325 | about 3,300 |
| Longer Kafka batch window | 0 | about 800 | 11,651 | 0 | 73 |
| Atomic driver claim | 0 | about 800 | 0 | 0 | 1 |

A replay means matching saw a ride that already had a driver, usually a retry after a publish timed out. The backlog shows matching couldn't keep up until the claim change. It grew in row 3, but this data doesn't isolate why.

## Cause 1: the load generator ran out of ports

**Symptom:** intake collapsed to 45 RPS at the 800 RPS step.

**Evidence:** `TIME_WAIT` sockets reached 16,279, close to Windows' default range of about 16,384 ephemeral ports. Kafka writes were still taking 20-35 ms.

**Cause:** `rider-simulator` never read response bodies and kept Go's default of 2 idle connections per host, so most requests opened a new TCP connection.

**Fix:** read each body to the end and allow 1,000 idle connections. `TIME_WAIT` fell to about 15 and rider errors to 0, with no change to the services.

## Cause 2: Kafka batches closed faster than they could be sent

**Symptom:** rides reached `ride.priced.dlq` with `context deadline exceeded`, and about half the matched events were published twice.

**Evidence:** writer stats showed writes taking 15-25 ms, but messages waiting 250-840 ms overall. The `ride.matched` writer was sending about 68 batches per second, one at a time, so it was close to saturated.

**Cause:** `kafka-go` closes a batch when its `BatchTimeout` expires and sends batches for a partition one at a time. With a 10 ms window and a 20 ms write, batches were created faster than they could be sent. The queue grew regardless of Kafka's health, passing the 300 ms publish budget. A timed-out publish can still reach the broker, so each retry added another copy and more load.

**Fix:** a 25 ms batch window, which must stay above a broker round trip, and a 2 s publish budget. Writes now take about 5 ms with 0 retries. Rides now take longer through the pipeline at low load, because each hop can wait for the window.

## Cause 3: locking drivers was the bottleneck

**Symptom:** about 298,000 lock attempts produced 5,905 matches in one run, and `ride.priced` built a backlog.

**Evidence:** a lock took about 36 µs inside Redis but about 108 ms as seen by the client. Redis had 997 clients connected. Trips across the network were the cost, not the commands.

**Cause:** each ride locked up to 5 drivers per round, then needed more calls to assign the winner and release the others. Every lock made a driver unavailable to other rides for at least a round trip, so slower calls caused more conflicts, which caused more calls.

**Fix:** one Lua script finds the nearest free driver and assigns them in a single step. Redis runs scripts one at a time, so two rides can't collide and locks aren't needed. Each ride now costs one Redis call. This removed the lock keys, parallel batches, phantom-lock release and tier shuffle.

**Trade-off:** the script builds key names at runtime, so it needs a single Redis instance, not Redis Cluster.

## Cause 4: Redis calls ignored their deadline

**Symptom:** match p95 was about 900 ms with a 500 ms deadline.

**Evidence:** a test against a server that never replies took 3 s, the `go-redis` default read timeout, despite a 50 ms context.

**Cause:** `go-redis` ignores context deadlines on socket reads and writes unless `ContextTimeoutEnabled` is set.

**Fix:** enable it. That test is now part of the suite and fails if the setting is turned off. A claim that times out is retried rather than reported as unmatched, because it may have succeeded in Redis. The retry returns the same driver.

## Also fixed along the way

- **Consumers stopped on brief Kafka errors.** A leader election at startup stopped all three consumer services. Fetch errors now retry with backoff.
- **Pricing capped the pipeline.** Its 8 workers each wait on a Kafka write. The default is now `NumCPU * 25`, set with `PRICING_WORKERS`.
- **Gateway publish errors weren't logged.** They are now.
- **`no_drivers` was misleading.** It also covered drivers that were only locked for a moment. It now means no free driver was in range.

## Results

Benchmark ladder with 1,000 drivers and 5 s trips. Full data: [`benchmark-results.csv`](../tools/benchmark/benchmark-results.csv).

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

E2E covers matched rides. Over the whole run, all 211,750 requests got a recorded result (52,608 matched, 159,142 unmatched), and none reached a dead-letter topic or stayed queued.

**What limits it now:**

- **Matched rides are limited by drivers.** Each driver is busy for a 5 s trip and returns on a 500 ms update tick, which fits the flat 160/sec. A separate 45 s run with 1 s trips reached about 450 matched/sec with no backlog.
- **Intake starts to strain near 1,500 RPS.** Gateway p95 reached 362 ms at the top step, and intake fell short of the 1,600 target. Everything shared one machine with Docker and a browser, so this may be a limit of the machine rather than the code.

**The cost:** at 50 RPS, median end-to-end latency rose from 40 ms to 91 ms because of the longer batch window, in exchange for stability under load.

**Limits of this evidence:** these numbers come from single runs on one shared machine, so treat them as indicative. Topics still have one partition.
