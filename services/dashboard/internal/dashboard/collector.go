package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"

	"ridepulse/pkg/events"
)

const (
	locationsKey   = "drivers:locations"
	scrapeTimeout  = 800 * time.Millisecond
	sourceTimeout  = 800 * time.Millisecond
	recordedFrames = 600 // ten minutes at one frame per second
)

// queueSource names one stage's Kafka consumer group and the topic it reads.
type queueSource struct {
	stage, group, topic string
}

var queueSources = []queueSource{
	{"pricing", "pricing-service", events.TopicRideRequested},
	{"matching", "matching-service", events.TopicRidePriced},
	{"assignment", "assignment-service", events.TopicRideMatched},
}

// Each metric the dashboard reads, by service.
var serviceMetrics = map[string]map[string]bool{
	"gateway":    {"gateway_requests_total": true},
	"pricing":    {"pricing_events_published_total": true, "pricing_surge_multiplier": true, "pricing_dlq_messages_total": true, "pricing_retries_total": true},
	"matching":   {"matching_success_total": true, "match_unmatched_published_total": true, "matching_dlq_messages_total": true, "matching_retries_total": true, "match_idempotent_replays_total": true},
	"assignment": {"assignments_total": true, "unmatched_total": true, "assignment_dlq_messages_total": true, "assignment_duplicates_total": true},
}

// Collector gathers live data from Kafka, Redis and each service's metrics, and
// produces one Frame per second. It reports what the system actually does: it
// only reads, apart from the demo controls.
type Collector struct {
	cfg    Config
	state  *State
	load   *Load
	rdb    *redis.Client
	kafka  *kafka.Client
	writer *kafka.Writer
	http   *http.Client

	counters map[string]*counter
	surge    float64

	mu          sync.Mutex
	subscribers map[chan []byte]struct{}
	recording   [][]byte
}

// NewCollector connects to Redis and Kafka.
func NewCollector(cfg Config, state *State, load *Load) *Collector {
	counters := make(map[string]*counter)
	for _, names := range serviceMetrics {
		for name := range names {
			counters[name] = &counter{}
		}
	}
	return &Collector{
		cfg:   cfg,
		state: state,
		load:  load,
		rdb:   redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, ContextTimeoutEnabled: true}),
		kafka: &kafka.Client{Addr: kafka.TCP(cfg.KafkaBrokers...), Timeout: sourceTimeout},
		writer: &kafka.Writer{
			Addr:         kafka.TCP(cfg.KafkaBrokers...),
			Balancer:     &kafka.Hash{},
			RequiredAcks: kafka.RequireOne,
			BatchTimeout: 10 * time.Millisecond,
		},
		http:        &http.Client{Timeout: scrapeTimeout},
		counters:    counters,
		surge:       1.0,
		subscribers: make(map[chan []byte]struct{}),
	}
}

// Close releases the connections.
func (c *Collector) Close() {
	c.rdb.Close()
	c.writer.Close()
}

// Run produces frames and follows the result topics until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	go follow(ctx, c.cfg.KafkaBrokers, events.TopicRideMatched, func(m kafka.Message) {
		var e events.RideMatchedEvent
		if json.Unmarshal(m.Value, &e) == nil {
			c.state.OnMatched(e, time.Now())
		}
	})
	go follow(ctx, c.cfg.KafkaBrokers, events.TopicRideUnmatched, func(m kafka.Message) {
		var e events.RideUnmatchedEvent
		if json.Unmarshal(m.Value, &e) == nil {
			c.state.OnUnmatched(e)
		}
	})
	go follow(ctx, c.cfg.KafkaBrokers, events.TopicRideRequested+".dlq", func(m kafka.Message) {
		c.state.Notice("Dead-letter topic received a message: " + headerValue(m, "x-error"))
	})

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.publish(c.frame(ctx, now))
		}
	}
}

func headerValue(m kafka.Message, key string) string {
	for _, h := range m.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

// follow reads new messages on partition 0 of a topic, which is the only
// partition the demo creates. It retries forever, so Kafka restarts pass by.
func follow(ctx context.Context, brokers []string, topic string, handle func(kafka.Message)) {
	r := kafka.NewReader(kafka.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MaxWait: 200 * time.Millisecond})
	defer r.Close()
	if err := r.SetOffset(kafka.LastOffset); err != nil {
		log.Printf("dashboard: %s: %v", topic, err)
	}
	for {
		m, err := r.ReadMessage(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("dashboard: read %s: %v", topic, err)
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}
		handle(m)
	}
}

func (c *Collector) frame(ctx context.Context, now time.Time) Frame {
	services := c.scrapeAll(ctx)
	for _, cn := range c.counters {
		cn.tick()
	}

	f := Frame{
		At:       now.UnixMilli(),
		Demand:   c.load.Current(now),
		Rush:     c.load.InRush(now),
		Surge:    c.surge,
		Queues:   c.queues(ctx),
		Services: services,
		Rates: Rates{
			Requested: c.rate("gateway_requests_total"),
			Priced:    c.rate("pricing_events_published_total"),
			Decided:   c.rate("matching_success_total") + c.rate("match_unmatched_published_total"),
			Matched:   c.rate("assignments_total"),
			Unmatched: c.rate("unmatched_total"),
		},
		Totals: Totals{
			Requested:  c.total("gateway_requests_total"),
			Matched:    c.total("assignments_total"),
			Unmatched:  c.total("unmatched_total"),
			DeadLetter: c.total("pricing_dlq_messages_total") + c.total("matching_dlq_messages_total") + c.total("assignment_dlq_messages_total"),
			Duplicates: c.total("assignment_duplicates_total"),
			Replays:    c.total("match_idempotent_replays_total"),
			Retries:    c.total("pricing_retries_total") + c.total("matching_retries_total"),
		},
	}
	c.state.FillFrame(&f, c.freeDrivers(ctx), now)
	return f
}

func (c *Collector) rate(name string) float64 { return c.counters[name].rate() }

func (c *Collector) total(name string) int64 { return int64(c.counters[name].total) }

// scrapeAll reads every service's metrics and reports which ones answered.
func (c *Collector) scrapeAll(ctx context.Context) map[string]bool {
	type result struct {
		service string
		values  map[string]float64
		ok      bool
	}
	results := make(chan result, len(serviceMetrics))
	for service, want := range serviceMetrics {
		go func() {
			values, err := c.scrape(ctx, c.cfg.MetricsURLs[service], want)
			results <- result{service, values, err == nil}
		}()
	}
	up := make(map[string]bool, len(serviceMetrics))
	for range serviceMetrics {
		r := <-results
		up[r.service] = r.ok
		for name, v := range r.values {
			if name == "pricing_surge_multiplier" {
				c.surge = v
				continue
			}
			c.counters[name].observe(v)
		}
	}
	return up
}

func (c *Collector) scrape(ctx context.Context, url string, want map[string]bool) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return parseMetrics(resp.Body, want)
}

// queues returns how many messages each stage has yet to finish, from Kafka's
// own offsets. A stage missing from the result couldn't be read.
func (c *Collector) queues(ctx context.Context) map[string]int64 {
	out := make(map[string]int64, len(queueSources))
	for _, q := range queueSources {
		lag, err := c.lag(ctx, q)
		if err == nil {
			out[q.stage] = lag
		}
	}
	return out
}

func (c *Collector) lag(ctx context.Context, q queueSource) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, sourceTimeout)
	defer cancel()
	end, err := c.kafka.ListOffsets(ctx, &kafka.ListOffsetsRequest{
		Topics: map[string][]kafka.OffsetRequest{q.topic: {kafka.LastOffsetOf(0)}},
	})
	if err != nil {
		return 0, err
	}
	committed, err := c.kafka.OffsetFetch(ctx, &kafka.OffsetFetchRequest{
		GroupID: q.group,
		Topics:  map[string][]int{q.topic: {0}},
	})
	if err != nil {
		return 0, err
	}
	endParts, committedParts := end.Topics[q.topic], committed.Topics[q.topic]
	if len(endParts) == 0 || len(committedParts) == 0 {
		return 0, fmt.Errorf("no offsets for %s", q.topic)
	}
	return lagOf(endParts[0].LastOffset, committedParts[0].CommittedOffset), nil
}

// lagOf is the messages between a group's committed offset and the end of the
// partition. A group that hasn't committed yet starts from the beginning.
func lagOf(end, committed int64) int64 {
	return max(0, end-max(0, committed))
}

// freeDrivers returns the drivers currently in Redis search, by name.
func (c *Collector) freeDrivers(ctx context.Context) map[string][2]float64 {
	ctx, cancel := context.WithTimeout(ctx, sourceTimeout)
	defer cancel()
	names, err := c.rdb.ZRange(ctx, locationsKey, 0, -1).Result()
	if err != nil || len(names) == 0 {
		return nil
	}
	positions, err := c.rdb.GeoPos(ctx, locationsKey, names...).Result()
	if err != nil {
		return nil
	}
	out := make(map[string][2]float64, len(names))
	for i, p := range positions {
		if p != nil {
			out[names[i]] = [2]float64{p.Latitude, p.Longitude}
		}
	}
	return out
}

// publish keeps the frame for the recording and sends it to every open page. A
// page that can't keep up misses frames rather than slowing the others.
func (c *Collector) publish(f Frame) {
	raw, err := json.Marshal(f)
	if err != nil {
		log.Printf("dashboard: encode frame: %v", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recording = append(c.recording, raw)
	if len(c.recording) > recordedFrames {
		c.recording = c.recording[1:]
	}
	for sub := range c.subscribers {
		select {
		case sub <- raw:
		default:
		}
	}
}

// Subscribe returns a channel of encoded frames and a function to stop.
func (c *Collector) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 4)
	c.mu.Lock()
	c.subscribers[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subscribers, ch)
		c.mu.Unlock()
	}
}

// Recording returns the kept frames, oldest first, as a JSON array.
func (c *Collector) Recording() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []byte{'['}
	for i, raw := range c.recording {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, raw...)
	}
	return append(out, ']')
}

// BadMessage publishes a message no service can read, to show dead-lettering.
func (c *Collector) BadMessage(ctx context.Context) error {
	return c.writer.WriteMessages(ctx, kafka.Message{
		Topic: events.TopicRideRequested,
		Key:   []byte("demo-bad-message"),
		Value: []byte("this is not a ride"),
	})
}

// Redeliver publishes the most recent matched ride's ride.priced event again, as
// Kafka would after a crash. It reports the ride and driver, or false if no ride
// has been matched yet.
func (c *Collector) Redeliver(ctx context.Context) (rideID, driverID string, ok bool, err error) {
	e, pickup, ok := c.state.LastRide()
	if !ok {
		return "", "", false, nil
	}
	raw, err := json.Marshal(events.RidePricedEvent{
		Version:         events.Version,
		RideID:          e.RideID,
		Price:           e.Price,
		Currency:        e.Currency,
		SurgeMultiplier: e.SurgeMultiplier,
		Pickup:          events.Location{Lat: pickup[0], Lng: pickup[1]},
		RequestedAt:     e.RequestedAt,
	})
	if err != nil {
		return "", "", true, err
	}
	err = c.writer.WriteMessages(ctx, kafka.Message{Topic: events.TopicRidePriced, Key: []byte(e.RideID), Value: raw})
	return e.RideID, e.DriverID, true, err
}
