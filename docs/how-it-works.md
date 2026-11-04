# How RidePulse works, in simple terms

This is the plain-language guide to the project: what it does, how each feature works, and what isn't built yet. For the technical detail, see the [README](../README.md) and the other notes in this folder.

## What it is

RidePulse is the behind-the-scenes system of a ride-hailing app. When someone asks for a ride, it works out a fare, finds a nearby free driver, and makes sure that driver isn't given to anyone else.

It's a backend only, with no app or map screen. Riders and drivers are simulated by small programs so the system can be put under heavy load.

## The journey of one ride

Think of a relay race, where each service does one job and passes the ride on through Kafka.

1. **A rider asks for a ride.** A pickup and drop location arrive at the API gateway. It checks the coordinates are valid, gives the ride an ID, and replies "requested".
2. **The ride gets a fare.** The pricing service calculates the price.
3. **The ride gets a driver.** The matching service searches for nearby drivers, reserves one and assigns them.
4. **The result is recorded.** The assignment service stores whether the ride was matched or not, and how long it took from start to finish.
5. **The driver finishes the trip.** After a set time, the simulated driver becomes free and starts appearing in searches again.

The rider doesn't wait for steps 2 to 4. They get their ride ID straight away and can look up the result later.

## The parts

| Part | What it does | Think of it as |
|---|---|---|
| **API gateway** | Takes ride requests over HTTP | The front desk |
| **Pricing service** | Works out the fare | The cashier |
| **Matching service** | Finds and assigns a driver | The dispatcher |
| **Assignment service** | Records each ride's outcome and answers "what happened to ride X?" | The logbook |
| **Driver simulator** | Pretends to be 1,000 drivers moving around Bengaluru | Fake drivers |
| **Rider simulator** | Sends ride requests at a chosen rate | Fake customers |
| **Benchmark tool** | Raises the load step by step and records how the system copes | The stress tester |
| **Kafka** | Carries messages between services and holds them until they're handled | A conveyor belt |
| **Redis** | Fast shared memory for driver locations and who is busy | The dispatcher's whiteboard |
| **Prometheus** | Collects measurements from every service | The dashboard's data source |

## Features and how they work

### Taking a ride request

The gateway rejects missing or impossible coordinates, such as a latitude of 200, and says which field is wrong. A value of 0 is valid, because the equator is a real place. If the ride can't be handed to Kafka, the rider gets an error instead of a false "requested".

### Calculating the fare

The fare is ₹50 plus ₹12 per km. Distance is measured straight over the Earth's surface from pickup to drop, not along roads. There's a surge field, but it's always 1.0 for now.

### Finding nearby drivers

Drivers send their location to Redis every half second, and Redis can answer "who is near this point?" very quickly. The matching service asks for the 20 closest drivers within 50 km.

### Not giving one driver to two riders

This is the hardest problem in the project. Many rides can want the same driver at the same moment, so a driver is claimed in two stages:

1. **Lock:** a short reservation that lasts 3 seconds. Only one ride can hold it, and Redis guarantees that even when requests arrive together. A driver who is already on a trip can't be locked.
2. **Assign:** the lock turns into a real assignment. The driver is marked busy, the ride is linked to them, and they're removed from search results. All of this happens as one step, so it can't half-finish.

### Trying several drivers at once

Trying drivers one at a time would be slow, so the matching service tries 5 at once and takes the first that succeeds. Any others it happened to lock are released straight away. Without that, they'd sit unusable for 3 seconds, which was a real bug in an earlier version (["phantom locks"](phantom-lock-fix.md)).

### Spreading out riders in a busy area

If everyone at a train station tried the same 5 closest drivers, most attempts would collide. So the 10 closest drivers are shuffled among themselves, then the next 10 among themselves. Riders spread out, but a far driver is never tried before a much closer group. See [the shuffle note](shuffle-nearest-first-fix.md).

### Time limits

Each ride gets 500 ms to find a driver, and each group of 5 gets 200 ms of that. A slow ride can't hold up the system forever, and it gets a clear "no driver" result instead of hanging. See [the deadline note](deadline-fix.md).

### Telling the system when no driver is found

A ride that can't be matched isn't silently dropped. It's recorded as unmatched with a reason: `no_drivers` or `match_deadline`. Under heavy load, `no_drivers` can also mean the nearest 20 drivers were all reserved by other rides at that moment.

### Never losing a ride

A service only marks a message as done after it has been handled. If a service crashes midway, Kafka delivers the message again. This also means a message can arrive twice, which leads to the next feature. See [the consumer note](at-least-once-consumers.md).

### Handling duplicates safely

If a ride that already has a driver arrives again, the matching service returns the same driver instead of assigning a second one. The assignment service also counts each ride only once.

### Handling messages that keep failing

A failed message is retried 3 times, waiting a little longer each time. If it still fails, it moves to a separate "dead letter" topic, along with the error, so someone can inspect it. The rest of the queue keeps moving.

### Drivers who stay busy

A matched driver is out of search results for the length of the trip (5 seconds by default in the simulator), then becomes available again. That makes driver supply limited, as it is in real life. See [the driver state note](driver-state-model.md).

### Surviving Kafka hiccups

If Kafka is briefly unavailable, for example while it's starting up, a service keeps retrying with growing pauses instead of quitting.

### Shutting down cleanly

When a service is told to stop, it finishes the work in hand, for up to 10 seconds, before exiting.

### Health checks

Each of the four main services answers two questions:

- `/health`: is the process alive?
- `/ready`: can it reach Kafka, and Redis if it uses it?

These checks only confirm a connection. They don't confirm that topics exist.

### Measuring everything

Every stage reports speed, success and failure counts to Prometheus. The gateway stamps each ride with its start time, so the final service can measure the whole journey rather than one step.

### Automatic checks on every change

GitHub CI runs `go vet`, `go build` and the tests for every module on each pull request.

## Trying it out

The README has the [full steps](../README.md#running-ridepulse). In short, start Kafka, Redis and Prometheus with Docker, start each service, then send load:

```bash
curl -X POST http://localhost:8080/rides -H "Content-Type: application/json" -d '{"pickup":{"lat":12.91,"lng":77.61},"drop":{"lat":12.97,"lng":77.59}}'
```

Use the returned ride ID to check the result:

```bash
curl http://localhost:2114/rides/<ride_id>
```

## What isn't built or proven yet

**Not yet proven**

- **No automated test with real Kafka and Redis.** The main flows were checked by hand on a live stack, but nothing repeats those checks automatically.
- **Performance comes from one run on one machine.** All rides matched at up to 100 requests per second, and it peaked at about 134 matched rides per second. Past that, rides increasingly collide over the same drivers.
- **It breaks down at very high load.** At a target of 800 requests per second, the gateway fell to about 45 and started returning errors. The cause hasn't been found.
- **A small share of rides can get stuck under overload.** In the benchmark, 213 of about 42,000 rides ended up in a dead letter topic instead of getting a result.

**Not built yet**

- **No surge pricing.** The multiplier is always 1.0.
- **Riders can't see their fare.** The price is calculated, but it isn't passed on to the final ride result.
- **Results are kept in memory.** Restarting the assignment service loses them. Lookups also go to that service directly, not through the gateway.
- **The search area is fixed.** It's always 50 km and doesn't grow when nearby drivers are scarce.
- **Silent drivers aren't detected.** A driver that stops sending updates stays in search results.
- **Rides stop at "matched".** There's no pickup, trip or completion step for a ride, and no cancelling.
- **Each Kafka topic has one partition.** This limits how much work can run in parallel.
- **No demo screen.** There's no map or dashboard to show someone.

## Glossary

- **Consumer group:** a team of readers sharing a Kafka topic, so each message goes to one of them.
- **Dead letter topic (DLQ):** a holding area for messages that kept failing.
- **Event:** a message saying something happened, such as "ride priced".
- **Idempotent:** doing it twice has the same result as doing it once.
- **Kafka topic:** a named queue of messages, such as `ride.requested`.
- **Lock:** a temporary reservation only one ride can hold.
- **p95 latency:** 95% of requests were this fast or faster.
- **RPS:** requests per second.
