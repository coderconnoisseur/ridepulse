beta benchmarking
@500RPS     
actual RPS ~126-192
Latency 260-390ms

The system is throttling requests , system is not generating 500RPS
Most likely caused by backpressure , I need to profile which microservice is causing the bottleneck and optimize it.