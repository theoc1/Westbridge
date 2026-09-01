// Command westbridge serves the web control panel for a single Asterisk
// ConfBridge conference.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dmalkin/westbridge/internal/ami"
	"github.com/dmalkin/westbridge/internal/conference"
	"github.com/dmalkin/westbridge/internal/web"
)

// shutdownTimeout bounds the graceful drain of in-flight HTTP requests.
const shutdownTimeout = 10 * time.Second

// errComponentFailed reports that the AMI client or the conference service
// stopped on its own, which is fatal: the process exits non-zero so a
// supervisor restarts it.
var errComponentFailed = errors.New("a background component stopped unexpectedly")

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

// run wires the application together and blocks until ctx is cancelled or the
// HTTP server fails.
//
// Nothing here depends on Asterisk being reachable: the AMI client retries in
// the background, and the API reports asteriskConnected: false in the
// meantime. A conference control panel that refuses to start because the PBX
// is down is exactly the tool you cannot use when the PBX is down.
func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	// ami.Client takes its state callback at construction while the service
	// needs the client, so the two are tied together through a closure. The
	// client is not running yet, so nothing can observe the nil.
	var svc *conference.Service

	client := ami.New(ami.Config{
		Addr:     cfg.AMIAddr,
		Username: cfg.AMIUser,
		Secret:   cfg.AMISecret,
		Logger:   logger.With("component", "ami"),
		OnStateChange: func(connected bool) {
			if svc != nil {
				svc.OnAMIStateChange(connected)
			}
		},
	})

	svc = conference.New(client, conference.Config{
		Room:              cfg.Room,
		OriginateContext:  cfg.OriginateContext,
		OriginateCallerID: cfg.OriginateCallerID,
		OriginateTimeout:  cfg.OriginateTimeout,
		ResyncInterval:    cfg.ResyncInterval,
		Logger:            logger.With("component", "conference"),
	})

	srv, err := web.New(svc, web.Config{Logger: logger.With("component", "web")})
	if err != nil {
		return err
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:    cfg.Listen,
		Handler: srv,
		// No write timeout: it would cut the WebSocket off mid-stream. The
		// socket has a keepalive of its own, and the read timeouts below
		// still bound a client that opens a connection and says nothing.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	// Bind before starting anything else, so a busy port is reported straight
	// away instead of after the AMI link has come up.
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}

	runCtx, stopComponents := context.WithCancel(ctx)
	defer stopComponents()

	// A component failure needs a signal of its own rather than just
	// cancelling runCtx: runCtx is also cancelled by SIGINT, and the two must
	// not be confused into reporting an orderly shutdown as a crash.
	failed := make(chan struct{})
	var failOnce sync.Once
	fail := func(what string, cause error) {
		logger.Error(what, "error", cause)
		failOnce.Do(func() { close(failed) })
		stopComponents()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Bad credentials and the like are terminal: without AMI the app can
		// do nothing, so bring the process down rather than serving a
		// permanently disconnected UI. An unreachable Asterisk is not this
		// case — the client just keeps retrying.
		if err := client.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			fail("ami client stopped", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := svc.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			fail("conference service stopped", err)
		}
	}()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", listener.Addr().String(), "room", cfg.Room)
		serveErr <- httpSrv.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case <-failed:
		err = errComponentFailed
	case serveError := <-serveErr:
		if !errors.Is(serveError, http.ErrServerClosed) {
			err = fmt.Errorf("http server: %w", serveError)
		}
	}

	// Drain the HTTP server first: in-flight kicks then still have a working
	// AMI connection to complete on.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancelShutdown()
	if shutdownErr := httpSrv.Shutdown(shutdownCtx); shutdownErr != nil {
		logger.Warn("http server did not shut down cleanly", "error", shutdownErr)
		_ = httpSrv.Close()
	}

	srv.Close() // disconnect any WebSocket that outlived the drain
	stopComponents()
	wg.Wait()

	return err
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("westbridge stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("westbridge stopped")
}
