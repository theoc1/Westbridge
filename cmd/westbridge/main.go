// Command westbridge serves the web control panel for a single Asterisk
// ConfBridge conference.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// config holds every knob the application exposes, all of them sourced from
// the environment. See README.md for the authoritative table.
type config struct {
	Listen            string
	AMIAddr           string
	AMIUser           string
	AMISecret         string
	Room              string
	OriginateContext  string
	OriginateCallerID string
	OriginateTimeout  time.Duration
	ResyncInterval    time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		Listen:            envOr("WB_LISTEN", ":8080"),
		AMIAddr:           envOr("WB_AMI_ADDR", "127.0.0.1:5038"),
		AMIUser:           os.Getenv("WB_AMI_USER"),
		AMISecret:         os.Getenv("WB_AMI_SECRET"),
		Room:              os.Getenv("WB_ROOM"),
		OriginateContext:  os.Getenv("WB_ORIGINATE_CONTEXT"),
		OriginateCallerID: envOr("WB_ORIGINATE_CALLERID", "Westbridge <0000>"),
	}

	var missing []string
	for _, req := range []struct {
		name  string
		value string
	}{
		{"WB_AMI_USER", cfg.AMIUser},
		{"WB_AMI_SECRET", cfg.AMISecret},
		{"WB_ROOM", cfg.Room},
		{"WB_ORIGINATE_CONTEXT", cfg.OriginateContext},
	} {
		if req.value == "" {
			missing = append(missing, req.name)
		}
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	var err error
	if cfg.OriginateTimeout, err = envDuration("WB_ORIGINATE_TIMEOUT", 30*time.Second); err != nil {
		return config{}, err
	}
	if cfg.ResyncInterval, err = envDuration("WB_RESYNC_INTERVAL", 30*time.Second); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: must be positive, got %s", name, d)
	}
	return d, nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// TODO(task 5): wire the AMI client, conference service, hub and HTTP server.
	logger.Info("configuration loaded",
		"listen", cfg.Listen,
		"amiAddr", cfg.AMIAddr,
		"room", cfg.Room,
		"originateContext", cfg.OriginateContext,
	)
}
