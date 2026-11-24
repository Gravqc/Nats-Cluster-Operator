package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

const seedCount = 100

// gap is an interval without a successful publish acknowledgement.
type gap struct {
	StartMillis int64 `json:"start_ms"`
	EndMillis   int64 `json:"end_ms"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func envInt(name string, fallback, lowest, highest int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < lowest || parsed > highest {
		return 0, fmt.Errorf("%s must be between %d and %d", name, lowest, highest)
	}
	return parsed, nil
}

// hold opens many connections without reconnection and reports when the server
// closed them, to time lame-duck client eviction.
func hold() error {
	count, err := envInt("CONNECTIONS", 100, 1, 2000)
	if err != nil {
		return err
	}
	seconds, err := envInt("HOLD_S", 300, 1, 1800)
	if err != nil {
		return err
	}
	closed := make(chan int64, count)
	connections := make([]*nats.Conn, 0, count)
	defer func() {
		for _, connection := range connections {
			connection.Close()
		}
	}()
	for i := 0; i < count; i++ {
		connection, err := nats.Connect(os.Getenv("NATS_URL"), nats.Token(os.Getenv("NATS_TOKEN")), nats.NoReconnect(),
			nats.DisconnectErrHandler(func(*nats.Conn, error) { closed <- time.Now().UnixMilli() }))
		if err != nil {
			return fmt.Errorf("connection %d: %w", i, err)
		}
		connections = append(connections, connection)
	}
	times := []int64{}
	deadline := time.After(time.Duration(seconds) * time.Second)
	for len(times) < count {
		select {
		case at := <-closed:
			times = append(times, at)
		case <-deadline:
			count = len(times)
		}
	}
	report := map[string]interface{}{"connections": len(connections), "closed": len(times)}
	if len(times) > 0 {
		first, last := times[0], times[0]
		for _, at := range times {
			first, last = min(first, at), max(last, at)
		}
		report["first_closed_ms"], report["last_closed_ms"] = first, last
	}
	output, err := json.Marshal(report)
	if err != nil {
		return err
	}
	fmt.Println(string(output))
	return nil
}

func run() error {
	if os.Getenv("MODE") == "hold" {
		return hold()
	}
	connection, err := nats.Connect(os.Getenv("NATS_URL"), nats.Token(os.Getenv("NATS_TOKEN")), nats.MaxReconnects(-1), nats.ReconnectWait(200*time.Millisecond))
	if err != nil {
		return err
	}
	defer connection.Close()
	js, err := connection.JetStream(nats.MaxWait(3 * time.Second))
	if err != nil {
		return err
	}
	// Optional padding makes each payload larger without changing its identity.
	// Seed, load and verify runs for one stream must use the same value.
	pad, err := envInt("PAD_BYTES", 0, 0, 1<<20)
	if err != nil {
		return err
	}
	payloadFor := func(i int) string {
		payload := fmt.Sprintf("%s-%d", os.Getenv("CLUSTER"), i)
		if pad > 0 {
			payload += "|" + strings.Repeat("x", pad)
		}
		return payload
	}
	mode := os.Getenv("MODE")
	if mode == "stepdown" {
		// Ask the current stream leader to step down so another replica leads.
		response, err := connection.Request("$JS.API.STREAM.LEADER.STEPDOWN.EVAL", nil, 5*time.Second)
		if err != nil {
			return err
		}
		var result struct {
			Success bool `json:"success"`
			Error   *struct {
				Description string `json:"description"`
			} `json:"error"`
		}
		if err = json.Unmarshal(response.Data, &result); err != nil {
			return err
		}
		if !result.Success {
			if result.Error != nil {
				return fmt.Errorf("stepdown failed: %s", result.Error.Description)
			}
			return fmt.Errorf("stepdown failed")
		}
		fmt.Println(`{"stepped_down":true}`)
		return nil
	}
	if mode == "seed" {
		replicas, _ := strconv.Atoi(os.Getenv("REPLICAS"))
		if replicas == 0 {
			replicas = 3
		}
		_, err = js.AddStream(&nats.StreamConfig{Name: "EVAL", Subjects: []string{"eval.events"}, Storage: nats.FileStorage, Replicas: replicas})
		if err != nil {
			return err
		}
	}
	if mode == "seed" || mode == "load" {
		count := seedCount
		start := 0
		if mode == "load" {
			count = 900
			// Keep pre-upgrade messages out of the load publisher so the final
			// integrity check can detect loss of those original messages.
			start = seedCount
		}
		if value := os.Getenv("COUNT"); value != "" && mode == "load" {
			count, err = strconv.Atoi(value)
			if err != nil || count < 1 || count > 100000 {
				return fmt.Errorf("COUNT must be between 1 and 100000")
			}
		}
		interval := 100 * time.Millisecond
		if value := os.Getenv("INTERVAL_MS"); value != "" {
			millis, parseErr := strconv.Atoi(value)
			if parseErr != nil || millis < 1 || millis > 1000 {
				return fmt.Errorf("INTERVAL_MS must be between 1 and 1000")
			}
			interval = time.Duration(millis) * time.Millisecond
		}
		// A duration bounds a load run by time instead of count. The message cap
		// still applies, and the output reports how many were acknowledged.
		duration, err := envInt("DURATION_S", 0, 0, 1800)
		if err != nil {
			return err
		}
		var deadline time.Time
		if mode == "load" && duration > 0 {
			count = 100000
			deadline = time.Now().Add(time.Duration(duration) * time.Second)
		}
		acked := 0
		timeouts := 0
		gaps := []gap{}
		maxGap := time.Duration(0)
		started := time.Now()
		lastAck := started
		for i := start; i < start+count; i++ {
			if !deadline.IsZero() && time.Now().After(deadline) {
				break
			}
			payload := payloadFor(i)
			// Retries keep one message ID, so an ambiguous acknowledgement is deduplicated.
			var ack *nats.PubAck
			for attempt := 0; attempt < 30; attempt++ {
				ack, err = js.Publish("eval.events", []byte(payload), nats.MsgId(fmt.Sprintf("%s-%d", os.Getenv("CLUSTER"), i)))
				if err == nil {
					break
				}
				timeouts++
				time.Sleep(200 * time.Millisecond)
			}
			if err != nil {
				return fmt.Errorf("publish %d failed after retries: %w", i, err)
			}
			if ack.Sequence == 0 {
				return fmt.Errorf("publish returned no sequence")
			}
			now := time.Now()
			if elapsed := now.Sub(lastAck); elapsed > maxGap {
				maxGap = elapsed
			}
			if now.Sub(lastAck) > time.Second {
				gaps = append(gaps, gap{StartMillis: lastAck.UnixMilli(), EndMillis: now.UnixMilli()})
			}
			lastAck = now
			acked++
			if mode == "load" {
				time.Sleep(interval)
			}
		}
		if mode == "load" && (pad > 0 || duration > 0) {
			report, err := json.Marshal(map[string]interface{}{
				"acknowledged": acked, "retry_errors": timeouts, "max_gap_ms": maxGap.Milliseconds(),
				"gaps": gaps, "started_ms": started.UnixMilli(), "ended_ms": lastAck.UnixMilli(),
			})
			if err != nil {
				return err
			}
			fmt.Println(string(report))
			return nil
		}
		fmt.Printf("{\"acknowledged\":%d,\"retry_errors\":%d}\n", acked, timeouts)
		return nil
	}
	info, err := js.StreamInfo("EVAL")
	if err != nil {
		return err
	}
	expected := seedCount
	if value := os.Getenv("EXPECTED"); value != "" {
		expected, _ = strconv.Atoi(value)
	}
	if info.State.Msgs != uint64(expected) {
		return fmt.Errorf("expected %d acknowledged messages, got %d", expected, info.State.Msgs)
	}
	for i := 0; i < expected; i++ {
		message, err := js.GetMsg("EVAL", uint64(i+1))
		if err != nil {
			return err
		}
		if string(message.Data) != payloadFor(i) {
			return fmt.Errorf("message %d differs or crossed clusters", i)
		}
	}
	fmt.Printf("{\"verified\":%d,\"missing\":0,\"cluster\":%q}\n", expected, os.Getenv("CLUSTER"))
	return nil
}
