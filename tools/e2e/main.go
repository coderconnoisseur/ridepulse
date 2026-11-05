// Command e2e runs load, a rush hour, a corrupt message, a duplicate and a crash
// against the running demo stack, then checks the system's guarantees against
// what is really on the Kafka topics. Run it with:
//
//	docker compose run --rm --build e2e
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/segmentio/kafka-go"

	"ridepulse/pkg/events"
)

const (
	gatewayURL   = "http://api-gateway:8080/rides"
	dashboardURL = "http://dashboard:3000"
	broker       = "kafka:9092"

	steadyRPS    = 40
	loadDuration = 45 * time.Second
	settleLimit  = 2 * time.Minute

	// Trips last 5 s and matched_at has whole seconds, so 4 s rules out rounding.
	minTripGap = 4 * time.Second

	rideBody = `{"pickup":{"lat":12.91,"lng":77.61},"drop":{"lat":12.97,"lng":77.59}}`
)

func main() {
	ctx := context.Background()

	// The test sends its own rides, so it knows exactly which were accepted.
	must(control("/api/demand?rps=0"))

	var accepted []string
	load := make(chan struct{})
	go func() {
		defer close(load)
		accepted = sendRides(loadDuration)
	}()

	time.Sleep(8 * time.Second)
	must(control("/api/bad-message"))
	must(retry(30*time.Second, func() error { return control("/api/redeliver") })) // needs one matched ride
	must(control("/api/rush"))
	time.Sleep(5 * time.Second)
	log.Print("force-killing matching during the rush")
	must(control("/api/crash"))
	<-load
	log.Printf("gateway accepted %d rides; waiting for every ride to get an outcome", len(accepted))
	if len(accepted) == 0 {
		log.Fatal("the gateway accepted no rides")
	}

	var got topics
	deadline := time.Now().Add(settleLimit)
	for {
		got = readTopics(ctx)
		missing := len(missingOutcomes(got))
		if missing == 0 || time.Now().After(deadline) {
			break
		}
		log.Printf("%d rides still in progress", missing)
		time.Sleep(5 * time.Second)
	}

	problems := audit(got, accepted, minTripGap)
	maxSurge := 0.0
	for _, e := range got.priced {
		maxSurge = max(maxSurge, e.SurgeMultiplier)
	}
	if maxSurge <= 1 {
		problems = append(problems, "surge never rose during the rush hour")
	}
	if !hasError(readAll(ctx, events.TopicRideRequested+".dlq")) {
		problems = append(problems, "the corrupt message did not reach ride.requested.dlq with its error")
	}

	log.Printf("rides=%d matched events=%d unmatched events=%d priced events=%d max surge=%.1f",
		len(got.requested), len(got.matched), len(got.unmatched), len(got.priced), maxSurge)
	if len(problems) > 0 {
		for _, p := range problems {
			log.Print("FAIL: ", p)
		}
		os.Exit(1)
	}
	log.Print("PASS: no ride lost, none given two drivers, no driver double-booked, outcomes and fares consistent")
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func retry(limit time.Duration, f func() error) error {
	deadline := time.Now().Add(limit)
	for {
		err := f()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Second)
	}
}

func control(path string) error {
	req, err := http.NewRequest(http.MethodPost, dashboardURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-RidePulse-Demo", "e2e")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return nil
}

// sendRides posts rides at steadyRPS and returns the IDs the gateway accepted.
func sendRides(duration time.Duration) []string {
	var (
		mu       sync.Mutex
		accepted []string
		wg       sync.WaitGroup
	)
	ticker := time.NewTicker(time.Second / steadyRPS)
	defer ticker.Stop()
	for end := time.Now().Add(duration); time.Now().Before(end); <-ticker.C {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if id, ok := sendRide(); ok {
				mu.Lock()
				accepted = append(accepted, id)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return accepted
}

func sendRide() (string, bool) {
	resp, err := http.Post(gatewayURL, "application/json", bytes.NewBufferString(rideBody))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var created struct {
		RideID string `json:"ride_id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&created)
	io.Copy(io.Discard, resp.Body)
	return created.RideID, err == nil && resp.StatusCode == http.StatusCreated
}

func readTopics(ctx context.Context) topics {
	return topics{
		requested: decode[events.RideRequestedEvent](readAll(ctx, events.TopicRideRequested)),
		priced:    decode[events.RidePricedEvent](readAll(ctx, events.TopicRidePriced)),
		matched:   decode[events.RideMatchedEvent](readAll(ctx, events.TopicRideMatched)),
		unmatched: decode[events.RideUnmatchedEvent](readAll(ctx, events.TopicRideUnmatched)),
	}
}

// decode skips what isn't an event, like the corrupt message this test sends.
func decode[T any](msgs []kafka.Message) []T {
	var out []T
	for _, m := range msgs {
		var e T
		if json.Unmarshal(m.Value, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// readAll returns everything on partition 0, the demo's only partition.
func readAll(ctx context.Context, topic string) []kafka.Message {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := kafka.DialLeader(ctx, "tcp", broker, topic, 0)
	must(err)
	defer conn.Close()
	first, last, err := conn.ReadOffsets()
	must(err)
	if first == last {
		return nil
	}
	r := kafka.NewReader(kafka.ReaderConfig{Brokers: []string{broker}, Topic: topic, Partition: 0})
	defer r.Close()
	must(r.SetOffset(first))
	msgs := make([]kafka.Message, 0, last-first)
	for {
		m, err := r.ReadMessage(ctx)
		must(err)
		msgs = append(msgs, m)
		if m.Offset == last-1 {
			return msgs
		}
	}
}

func hasError(msgs []kafka.Message) bool {
	for _, m := range msgs {
		for _, h := range m.Headers {
			if h.Key == "x-error" && len(h.Value) > 0 {
				return true
			}
		}
	}
	return false
}
