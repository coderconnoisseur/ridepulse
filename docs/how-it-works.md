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

Drivers send their location to Redis every half second, and Redis can answer "who is near this point?" very quickly. The search covers 50 km and starts from the closest driver.

### Not giving one driver to two riders

Many rides can want the same driver at the same moment. So finding a driver and assigning them happen as one step inside Redis: find the nearest free driver, mark them busy, link them to the ride, and remove them from search results.

Redis does these steps one at a time, so two rides can never pick the same driver, and nothing can half-finish.

An earlier version reserved drivers with temporary locks. Under heavy load that became the bottleneck, so it was replaced. [This write-up](matching-under-load.md) explains how that was found.

### Time limits

Each attempt to claim a driver gets 500 ms. If it runs out of time, the attempt is retried. That's safe because a retry for the same ride returns the same driver.

### Telling the system when no driver is found

A ride that can't be matched isn't silently dropped. It's recorded as unmatched with the reason `no_drivers`.

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
- **Performance comes from one run on one machine.** It accepted about 1,500 requests per second, and every ride got a result. Matched rides levelled off at about 160 per second because the 1,000 simulated drivers are busy for 5 seconds per trip.
- **The true top speed isn't known.** Things slow down near 1,500 requests per second, but the services, Docker and the load generator all shared one machine, so that may be the machine's limit.

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
- **p95 latency:** 95% of requests were this fast or faster.
- **RPS:** requests per second.
