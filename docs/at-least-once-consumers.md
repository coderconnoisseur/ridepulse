# At-Least-Once Consumers — pricing, matching, assignment

> **Status:** local-only note. Not intended for the public repo.
> Documents the offset-commit and failure-handling bug shared by the three
> Kafka consumers, the shared runner that replaced it
> (`pkg/kafkaconsumer`), the epoch mechanism that handles consumer-group
> rewinds, handler panic recovery, and what is still not covered.

---

## 1. The bug in one paragraph

All three consumers (pricing, matching, assignment) used
`FetchMessage` plus an explicit `CommitMessages`, but the handling around
them lost messages in two ways. First, the handler was a worker-pool
`Enqueue`/`Submit` that returned `nil` as soon as the event was buffered, so
the offset was committed *before* processing: a crash lost everything
buffered or in flight, and a handler that later failed was only logged.
Second, on the synchronous path a failing handler hit `continue` without
committing, but the next message that did succeed committed a later offset,
which implicitly covers the skipped one, so the "retry" never happened. The
code also had no notion of which messages were actually finished, so there
was no safe offset to commit. The result was at-most-once delivery with
silent drops, which is the wrong default for a pipeline whose only success
signal is "the ride eventually shows up in assignment-service".

---

## 2. Root cause

Three things were conflated:

1. **Fetching** a message (it is now ours).
2. **Handling** it (it may fail, and runs concurrently with its neighbours).
3. **Committing** it (a promise to the broker that it never needs to be
   delivered again).

Committing when work is merely enqueued makes (3) happen before (2). Committing on handler
completion from a worker pool is also wrong on its own: workers finish out
of order, and a Kafka commit is a single number per partition ("everything
below this offset is done"). Committing offset 10 because message 10
finished first silently tells the broker that messages 7–9, still running,
are done too.

---

## 3. The fix: `pkg/kafkaconsumer`

One runner, used by all three services. Each service supplies a
`kafka.Reader`, a DLQ `kafka.Writer`, a worker count and a handler; the
runner owns fetching, retries, dead-lettering, commits and shutdown.

### 3a. Ordered-commit watermark

`offsetTracker` (`pkg/kafkaconsumer/tracker.go`) keeps, per topic-partition,
the fetched-but-uncommitted messages in fetch order plus the set of offsets
that have finished. When a message finishes, the tracker pops the
contiguous finished prefix and returns the *last* popped message; only that
one is committed.

Why committing the higher offset is safe: a consumer-group commit means
"resume after this offset", so it implies every lower offset of the
partition. The tracker therefore only releases offset N once every message
fetched before N on the same partition has also reached a terminal state.
A slow message at the head holds the watermark back; later messages that
already finished stay in the `done` set and are committed in one go as soon
as the head completes. Commits are serialized under a mutex, so the
committed offset of a partition is monotonic within an epoch (see 3f).

A message reaches a terminal state in exactly one of three ways:

| Outcome | Terminal because |
|---|---|
| Handler returned `nil` | Handled. |
| Handler failed (after retries, or immediately for `Permanent`) and the DLQ write succeeded | Dead-lettered; the failure is preserved elsewhere. |
| Handler failed and no DLQ is configured | Logged and dropped (not used by the three services; they all set a DLQ). |

### 3b. Retries

`handleWithRetry` calls the handler up to `1 + MaxRetries` times
(`MaxRetries` defaults to 3) with exponential backoff starting at 50 ms and
capped at 1 s. An error wrapped with `kafkaconsumer.Permanent` skips the
retries. All three consumers wrap JSON decode failures this way: a payload
that cannot be decoded will not decode on attempt four either. Everything
else, including a failed Kafka publish downstream, is retried.

Pricing and matching count retries (`pricing_retries_total`,
`matching_retries_total`) through the `OnRetry` hook. Assignment's handler
cannot fail after decode, so it does not wire the hook.

### 3c. Dead-letter topics

The DLQ topic is the original topic plus `.dlq`:

| Source topic | DLQ topic | Written by |
|---|---|---|
| `ride.requested` | `ride.requested.dlq` | pricing-service |
| `ride.priced` | `ride.priced.dlq` | matching-service |
| `ride.matched` | `ride.matched.dlq` | assignment-service |
| `ride.unmatched` | `ride.unmatched.dlq` | assignment-service |

The DLQ record keeps the original key and value and any original headers,
and appends four more:

| Header | Value |
|---|---|
| `x-error` | `err.Error()` of the last handler failure |
| `x-original-topic` | topic the message came from |
| `x-original-partition` | partition, decimal |
| `x-original-offset` | offset, decimal |

That is enough to replay a record by hand (strip the `x-*` headers, publish
the value back to the original topic) and to find the exact source record.

The DLQ writers set `BatchTimeout: 10ms` (the 1 s default would make every
synchronous DLQ write wait about a second) and `AllowAutoTopicCreation: true`, because the DLQ topics
are not provisioned anywhere else; the first dead-lettered message creates
the topic. The runner sets `Topic` on each DLQ message, so the writer itself
has no fixed topic.

**A DLQ write that still fails after `MaxRetries` is fatal.** The runner
records the error, stops fetching, drains, and `Run` returns it, so the
service exits. The message is left uncommitted and is redelivered after the
restart. This is deliberately crash-only: the alternative is dropping a
message we could not preserve.

### 3d. Drain on shutdown

On SIGINT/SIGTERM the service context is cancelled. Then:

1. The fetch loop stops. A message fetched but not yet handed to a worker
   stays tracked as pending, which blocks commits past it.
2. Handlers are **not** cancelled. They run on a context detached from the
   service context, so in-flight messages can finish, publish and commit.
3. `Run` waits up to `DrainTimeout` (10 s default) for the workers.
4. If the timeout expires, the handler context is cancelled and the runner
   stops committing. Whatever was in flight stays uncommitted and is
   redelivered. A "drain timeout ... leaving in-flight messages
   uncommitted" line is logged.

`Run` never closes the reader or the DLQ writer. After the runner drains,
each service's `app.Run` shuts down its HTTP server, then closes the reader,
the DLQ writer and the publisher (matching also closes Redis). Fatal errors
(a fetch error not caused by shutdown, a synchronous commit error, a DLQ
write failure) take the same drain path and are returned to `main`.

### 3e. Handler panics

Each handler attempt runs under a `recover`. A panic is logged with its
stack, wrapped as `Permanent(handler panic: ...)` and treated like any other
permanent failure: no retries, straight to the DLQ, offset committed once
the DLQ write succeeds. A handler that panics deterministically therefore
costs one DLQ record per message instead of a crash loop. The panic text ends
up in the record's `x-error` header, and the service's `*_dlq_messages_total`
counter increments (the `OnRetry` hook does not fire, since there is no
retry). Only panics on the handler goroutine are caught; a panic in a
goroutine the handler itself starts still crashes the process.

### 3f. Rewinds and epochs

A consumer-group rebalance makes kafka-go re-fetch from the last committed
offset, which can be lower than offsets the tracker already holds. Left
alone, the old pending entries and the new copies would collide and the
watermark would stall.

`partitionState` therefore carries an `epoch` and `last`, the highest offset
tracked in the current epoch. `track` compares each fetched offset with
`last`:

- offset greater than `last`: ordinary forward fetch, same epoch;
- offset at or below `last`: the partition rewound. The tracker logs it,
  increments the epoch, discards the old `pending` and `done` state and starts
  tracking from the re-fetched message.

`track` returns the epoch, the runner carries it with the message to its
worker, and `done(msg, epoch)` ignores a completion whose epoch is not the
current one (or whose offset is no longer pending). Copies that were in
flight when the rewind happened still run to completion, but cannot move the
watermark or trigger a commit; the re-fetched copy is processed again and
commits normally. The cost is that such messages are handled twice, which the
handlers already tolerate (section 5).

What this does **not** detect: a rewind is only noticed when the re-fetched
offset is `<= last`. If a partition is re-assigned to this consumer starting
*above* the last tracked offset, nothing looks wrong to the tracker, the old
pending entries stay, and the committed offset can end up behind where it
was. Messages between the two are redelivered. That is still at-least-once,
but it is redelivery the tracker did not recognize and could not suppress.

### 3g. Reader settings

All three readers use `CommitInterval: 1s`. `CommitMessages` therefore
queues the offset and a background loop flushes it once a second, instead
of paying one broker round trip per message. With an interval,
`CommitMessages` does not return commit errors; kafka-go reports them
through `ReaderConfig.ErrorLogger` and retries the pending offset on the
next tick. All three readers set `ErrorLogger` to the standard logger
(prefixed with the service name) so failed commits are visible. The runner's
fatal-commit path therefore only applies to synchronous commits (interval
0). The cost: after a crash, up to
about one second of already-handled messages can be redelivered on top of
the in-flight ones. That is within the at-least-once contract, and it is
why the handlers have to tolerate duplicates (section 5).

---

## 4. Per-service behaviour

| Service | Workers | Decode failure | Handler failure |
|---|---|---|---|
| pricing | `NumCPU` | Permanent → `ride.requested.dlq` | Publish error retried, then DLQ |
| matching | `MATCH_WORKERS` (default `NumCPU × 25`) | Permanent → `ride.priced.dlq` | See below |
| assignment (`ride.matched`) | 4 | Permanent → `ride.matched.dlq` | Cannot fail after decode |
| assignment (`ride.unmatched`) | 4 | Permanent → `ride.unmatched.dlq` | Cannot fail after decode |

All handler panics, in any service, take the permanent path (3e).

assignment-service runs two runners, one per topic, with two readers in the
same consumer group (`assignment-service`). Each topic's partitions are
assigned correctly, but a reader joining or leaving triggers a rebalance
that affects both. If one runner fails fatally the other is cancelled and
`Run` returns the first error.

**Matching** splits `Match` errors in `app.go`:

- `ErrNoDriversAvailable`, and `context.DeadlineExceeded` when it is the
  outer match deadline (`matchCtx.Err() != nil`), are business failures.
  The handler logs and publishes a `ride.unmatched` event (reason
  `no_drivers` or `match_deadline`, 300 ms publish budget), then returns
  `nil` and the offset is committed. If that publish fails the error is
  returned, so the runner retries and eventually dead-letters it like any
  other infrastructure error. See
  [driver-state-model.md](driver-state-model.md#4b-the-unmatched-path).
- Any other `Match` error (Redis failures, and any other
  `DeadlineExceeded` such as the detached assign timeout) is returned, so the runner
  retries it and eventually dead-letters it. Retrying is safe because
  `Match` replays an existing assignment instead of matching again
  (see [driver-state-model.md](driver-state-model.md)).
- Publishing `ride.matched` has its own 300 ms budget; a publish failure is
  returned, retried and dead-lettered like any other infrastructure error.

---

## 5. Trade-offs and known behaviour

- **Duplicates are possible.** A crash after the handler succeeded but
  before the 1 s commit flush redelivers the message, as does a rewind
  (3f). Pricing is a pure function plus a publish, so a duplicate
  `ride.priced` is harmless to pricing itself but flows downstream. Matching
  deduplicates by ride through `ride:driver:<rideID>`. Assignment-service
  deduplicates in its store: the first `ride.matched` for a ride is recorded
  and counted in `assignments_total` and `end_to_end_latency_seconds`; later
  ones only increment `assignment_duplicates_total`. A ride has one outcome:
  matched wins over unmatched (a `ride.matched` replaces an earlier
  unmatched record, a later `ride.unmatched` is ignored and counted in
  `unmatched_duplicates_total`). The store is in memory, so after an
  assignment-service restart a redelivered event is new again.
- **One slow message delays commits, not processing.** Other workers keep
  going; the watermark simply does not advance past the stuck message, so a
  crash replays everything after it. Retries are bounded (4 attempts, ≤ 1 s
  backoff), so a stuck head is bounded too.
- **Rewind detection is partial.** Epochs (3f) handle a rewind to an offset
  `<= last`. A partition re-assigned starting above the last tracked offset
  is not detected, and the committed offset can move backwards, causing
  redelivery. At-least-once still holds. The topics are single-partition and
  each service runs as a single instance today, so neither case has been
  seen outside unit tests.
- **Panic recovery covers the handler goroutine only** (3e). A panic in a
  goroutine the handler spawns is not caught.
- **Drain timeout abandons work.** After `DrainTimeout` the runner returns
  without waiting for stuck handlers; they are cancelled and their messages
  are redelivered.

---

## 6. How to observe it

| Metric | Meaning |
|---|---|
| `pricing_retries_total`, `matching_retries_total` | Handler attempts that failed and were retried. |
| `pricing_dlq_messages_total`, `matching_dlq_messages_total`, `assignment_dlq_messages_total` | Messages written to the service's DLQ topic. Should stay at zero in a healthy run. |
| `match_idempotent_replays_total` | Matching saw a ride that already had a driver (retry or redelivery). Non-zero is evidence of redelivery being absorbed. |
| `match_unmatched_published_total{reason}` | `ride.unmatched` events matching published, by `no_drivers` / `match_deadline`. |
| `assignment_duplicates_total`, `unmatched_duplicates_total` | Redelivered `ride.matched` / `ride.unmatched` events assignment-service recognized and ignored. |
| `pricing_events_consumed_total` | Handler **attempts**, retries included. Compare with `pricing_events_published_total`. |

```promql
rate(pricing_retries_total[1m])
rate(matching_retries_total[1m])
increase(matching_dlq_messages_total[5m])
rate(match_idempotent_replays_total[1m])
```

To inspect dead letters, consume the `*.dlq` topic and read the `x-error`
and `x-original-*` headers. To check what is committed, describe the
consumer groups `pricing-service`, `matching-service` and
`assignment-service` with the Kafka CLI: lag should return to zero after a
clean shutdown and restart.

---

## 7. What remains

- **Full rewind detection.** Re-assignment above the last tracked offset is
  not recognized (3f).
- **Phase 2 throughput work.** Multi-partition topics, and the batch-1
  fail-fast for slow-fail matches in matching. The `MATCH_WORKERS` /
  `MATCH_DEADLINE` defaults (`NumCPU × 25`, 500 ms) are unmeasured.
- **Persistent assignment store**, so dedupe survives a restart.
- **No end-to-end run yet.** The epoch handling, panic path, `ride.unmatched`
  flow and assignment dedupe are covered by hermetic unit tests only; none of
  it has been run against real Kafka and Redis.
- **Benchmark ladder must be re-run.** `tools/benchmark/benchmark-results.csv`
  predates these changes; see the note in the README.
