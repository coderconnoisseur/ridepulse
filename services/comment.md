Made some changes , for example :
increased the number of workers in worker pool to 5 times the number of CPU cores, added context to the DriverRepository interface methods.
Increased the time to wait for a driver to be locked to 400 milliseconds in the matching service.

Metrics:
250–350 RPS
Latency: 600ms–900ms
Errors: 0

The drivers are getting locked .
logs:
2026/02/18 15:39:39 Found 20 nearby drivers
2026/02/18 15:39:39 Trying driver: driver-35
2026/02/18 15:39:39 Found 20 nearby drivers
2026/02/18 15:39:39 Trying driver: driver-616
2026/02/18 15:39:39 Trying driver: driver-347
2026/02/18 15:39:39 Trying driver: driver-11
2026/02/18 15:39:39 Trying driver: driver-8

Drivers exist
But they get locked by other concurrent rides before ride can get them <=> High (resource)contention in a hot region
Progressive batching did help to reduce the contention but it is still high in some cases. 
Occasional spikes in latency and drop in RPS are observed due to this contention.

but logs show some drivers are getting hit repeatedly , they're hot-drivers (getting hit repeatedly) .

thought:
maybe instead of shuffling entire list , i can add random offset to the starting index and then iterate circularly .
Also, about failure strategy , current failure strategy is to fail immediately if driver lock fails , 
maybe i can add strategy such as retry or queuing or expanding radius or smth else .

I'm still facing backpressure.
