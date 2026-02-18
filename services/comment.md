beta benchmarking
@500RPS     

This time with increased radius to 50 KM so that we have more drivers to choose from and see how the system performs under load.

logs:
026/02/18 01:09:34 Trying driver: driver-186 2026/02/18 01:09:34 Trying driver: driver-985 2026/02/18 01:09:34 Trying driver: driver-930 2026/02/18 01:09:34 Trying driver: driver-875 2026/02/18 01:09:34 Trying driver: driver-391 2026/02/18 01:09:34 Locked driver: driver-186 2026/02/18 01:09:34 Trying driver: driver-786 2026/02/18 01:09:34 Trying driver: driver-805

RPS dropped , latency exploded 

analysis:
In small radius testing , fewer radius operation took place , so even if there was a failure , it would be very quick.
In large radius testing  many Redis lookup and lock attempts were made , so more CPU  + network

For each ride, matching is trying multiple locks sequentially.

Each lock = Redis roundtrip

Under load, many locks fail

Each retry adds latency

Worker pool gets blocked

Backpressure propagates to API

Kafka waits longer

RPS drops
BOTTLENECK : SEQUENTIAL LOCKING IN MATCHING SERVICE
