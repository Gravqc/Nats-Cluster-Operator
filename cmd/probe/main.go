package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
)

const seedCount = 100

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	connection, err := nats.Connect(os.Getenv("NATS_URL"), nats.Token(os.Getenv("NATS_TOKEN")), nats.MaxReconnects(-1), nats.ReconnectWait(200*time.Millisecond))
	if err != nil {
		return err
	}
	defer connection.Close()
	js, err := connection.JetStream(nats.MaxWait(3 * time.Second))
	if err != nil {
		return err
	}
	mode := os.Getenv("MODE")
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
		acked := 0
		timeouts := 0
		for i := start; i < start+count; i++ {
			payload := fmt.Sprintf("%s-%d", os.Getenv("CLUSTER"), i)
			// Retries keep one message ID, so an ambiguous acknowledgement is deduplicated.
			var ack *nats.PubAck
			for attempt := 0; attempt < 30; attempt++ {
				ack, err = js.Publish("eval.events", []byte(payload), nats.MsgId(payload))
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
			acked++
			if mode == "load" {
				time.Sleep(interval)
			}
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
		want := fmt.Sprintf("%s-%d", os.Getenv("CLUSTER"), i)
		if string(message.Data) != want {
			return fmt.Errorf("message %d differs or crossed clusters", i)
		}
	}
	fmt.Printf("{\"verified\":%d,\"missing\":0,\"cluster\":%q}\n", expected, os.Getenv("CLUSTER"))
	return nil
}
