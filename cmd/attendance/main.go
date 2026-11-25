// Command attendance runs the Mattermost attendance bot service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // embed zone data: the distroless image has none

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
	"github.com/vairabarath/Mattermost-attendance/internal/config"
	"github.com/vairabarath/Mattermost-attendance/internal/handlers"
	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
	"github.com/vairabarath/Mattermost-attendance/internal/store"
	"github.com/vairabarath/Mattermost-attendance/migrations"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit (for Docker HEALTHCHECK)")
	flag.Parse()

	if *healthcheck {
		os.Exit(probe())
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := connectWithRetry(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(ctx, migrations.FS, log); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	mm := mattermost.NewClient(cfg.MMURL, cfg.BotToken)
	svc := &attendance.Service{
		Store:     db,
		Directory: mm,
		Teams:     cfg,
		Loc:       cfg.Location,
		Log:       log,
		Now:       time.Now,
	}
	h := &handlers.Handler{Teams: cfg.Teams, Commander: svc, DB: db, Log: log}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		names := make([]string, 0, len(cfg.Teams))
		for _, t := range cfg.Teams {
			names = append(names, t.Name)
		}
		log.Info("listening", "addr", cfg.ListenAddr, "teams", strings.Join(names, ", "), "timezone", cfg.Location.String())
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// connectWithRetry waits for Postgres to accept connections (up to ~60s).
func connectWithRetry(ctx context.Context, url string, log *slog.Logger) (*store.Store, error) {
	db, err := store.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("database config: %w", err)
	}
	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = db.Ping(pingCtx)
		cancel()
		if err == nil {
			return db, nil
		}
		if attempt >= 20 || ctx.Err() != nil {
			db.Close()
			return nil, fmt.Errorf("database unreachable: %w", err)
		}
		log.Warn("database not ready, retrying", "attempt", attempt)
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
		}
	}
}

// probe is used by the Docker healthcheck (distroless has no curl).
func probe() int {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, errors.New(resp.Status))
		return 1
	}
	return 0
}
