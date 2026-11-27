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
	"github.com/vairabarath/Mattermost-attendance/internal/github"
	"github.com/vairabarath/Mattermost-attendance/internal/handlers"
	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
	"github.com/vairabarath/Mattermost-attendance/internal/pluginbridge"
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
	// Buttons stay valid for 2 days: long enough for any session.
	signer := mattermost.NewSigner(cfg.HMACKey, 48*time.Hour)
	statusUpdater := attendance.NewStatusUpdater(mm, db, log)
	go statusUpdater.Run(ctx)

	svc := &attendance.Service{
		Store:     db,
		Directory: mm,
		Teams:     cfg,
		Status:    statusUpdater,
		Signer:    signer,
		ActionURL: cfg.ServiceURL + "/mm/action",
		Admins:    attendance.NewAdminChecker(mm, 60*time.Second),
		People:    mm,
		Roster:    mm,
		Files:     mm,
		Workdays:  cfg.Workdays,
		Posts:     mm,
		DialogURL: cfg.ServiceURL + "/mm/dialog",
		TeamCfg:   cfg.Teams,
		Loc:       cfg.Location,
		Log:       log,
		Now:       time.Now,
	}
	// Interfaces stay nil (not typed nils) when GitHub is off.
	var teamTasks attendance.TeamTaskSource
	var board attendance.BoardSweeper
	if tasks := newTaskSource(cfg, log); tasks != nil {
		svc.Tasks = tasks
		svc.Writer = tasks
		teamTasks = tasks
		if cfg.GitHubAddAssigned && cfg.GitHubSweepMinutes > 0 {
			board = tasks
		}
		go tasks.Warm(ctx)
	}
	svc.StartEODSync(ctx)
	if cfg.PluginSecret != "" {
		n := pluginbridge.NewNotifier(cfg.MMURL, cfg.PluginSecret, log.With("component", "plugin"))
		n.Start(ctx)
		svc.Notify = n.Notify
		log.Info("panel api on", "plugin", pluginbridge.PluginID)
	}
	jobs := &attendance.Jobs{
		Store:      db,
		Roster:     mm,
		Msg:        mm,
		Status:     statusUpdater,
		Teams:      cfg.TeamList(),
		TeamNames:  cfg,
		Workdays:   cfg.Workdays,
		MaxSession: time.Duration(cfg.MaxSessionHours) * time.Hour,
		Leave:      attendance.NoLeave{},
		Loc:        cfg.Location,
		Log:        log.With("component", "jobs"),
		Now:        time.Now,
		EODRetry:   svc.RetryEODSyncs,
		GitHub:     teamTasks,
		Links:      mm,
		Notify:     svc.Notify,
	}
	if board != nil {
		jobs.Board = board
		jobs.SweepEvery = time.Duration(cfg.GitHubSweepMinutes) * time.Minute
	}
	stopJobs := jobs.Start(ctx)
	defer stopJobs()

	h := &handlers.Handler{Teams: cfg.Teams, Commander: svc, DB: db, Verifier: signer, Log: log,
		Panel: svc, PluginSecret: cfg.PluginSecret}

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

// newTaskSource builds the GitHub task source, or nil when GitHub is off
// or the App key can't be loaded. GitHub problems never stop attendance.
func newTaskSource(cfg *config.Config, log *slog.Logger) *github.Tasks {
	if !cfg.GitHubEnabled() {
		log.Info("github integration off (GITHUB_APP_ID not set)")
		return nil
	}
	key, err := github.LoadPrivateKey(cfg.GitHubPrivateKeyPath)
	if err != nil {
		log.Error("github integration off: can't load the App private key", "err", err)
		return nil
	}
	glog := log.With("component", "github")
	teams := map[string]github.TeamProject{}
	for id, t := range cfg.Teams {
		if t.GitHubOrg != "" {
			teams[id] = github.TeamProject{Org: t.GitHubOrg, Number: t.GitHubProject}
		}
	}
	app := github.NewApp(cfg.GitHubAPIURL, cfg.GitHubAppID, key, glog)
	tasks := github.NewTasks(app, teams, glog)
	tasks.AddAssigned = cfg.GitHubAddAssigned
	return tasks
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
