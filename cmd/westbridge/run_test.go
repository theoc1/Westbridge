package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

// freeAddr reserves a loopback port and releases it again. There is a race in
// principle, but the alternative is threading a listener through run() purely
// for the tests.
func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return addr
}

// TestRunServesWithoutAsterisk is the promise that the control panel is usable
// when the PBX is down: the process must come up, serve the API, and report
// the link as down rather than refusing to start.
func TestRunServesWithoutAsterisk(t *testing.T) {
	listen := freeAddr(t)
	cfg := config{
		Listen: listen,
		// Port 1 on loopback refuses connections immediately, so the client
		// spends the test reconnecting instead of blocking on a dial.
		AMIAddr:           "127.0.0.1:1",
		AMIUser:           "westbridge",
		AMISecret:         "secret",
		Room:              "1000",
		OriginateContext:  "conference-out",
		OriginateCallerID: "Westbridge <0000>",
		OriginateTimeout:  30 * time.Second,
		ResyncInterval:    time.Hour,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	body := getWithRetry(t, "http://"+listen+"/api/conference")

	var snap struct {
		Room              string `json:"room"`
		AsteriskConnected bool   `json:"asteriskConnected"`
		Participants      []any  `json:"participants"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	if snap.Room != "1000" {
		t.Errorf("room = %q, want 1000", snap.Room)
	}
	if snap.AsteriskConnected {
		t.Error("asteriskConnected = true, want false with no Asterisk running")
	}
	if snap.Participants == nil {
		t.Error("participants = null, want an empty array")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v, want nil on a clean shutdown", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after the context was cancelled")
	}

	// The port must be free again once run has returned.
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatalf("listener still held after shutdown: %v", err)
	}
	_ = ln.Close()
}

func getWithRetry(t *testing.T, url string) []byte {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(url) //nolint:gosec // the URL is built by the test
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				t.Fatalf("reading %s: %v", url, readErr)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: status %d: %s", url, resp.StatusCode, body)
			}
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s never succeeded: %v", url, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
