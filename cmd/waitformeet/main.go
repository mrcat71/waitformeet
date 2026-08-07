// Command waitformeet serves a countdown site for two people apart.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	// The whole site is about what time it is in two places, so a missing timezone
	// database is fatal to its purpose. Embedding it removes any dependency on the
	// base image carrying one, which distroless static does not. The system copy is
	// still preferred when present; this is only the fallback.
	_ "time/tzdata"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/config"
	"github.com/mrcat71/waitformeet/internal/i18n"
	"github.com/mrcat71/waitformeet/internal/store"
	"github.com/mrcat71/waitformeet/internal/users"
	"github.com/mrcat71/waitformeet/internal/web"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	// writeTimeout is generous because the export endpoint streams a zip of the
	// whole database and media directory.
	writeTimeout    = 5 * time.Minute
	idleTimeout     = 120 * time.Second
	shutdownTimeout = 20 * time.Second
	// housekeepingInterval controls how often expired sessions and invites are swept.
	housekeepingInterval = time.Hour
)

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet if configuration failed, so report to stderr.
		fmt.Fprintln(os.Stderr, "waitformeet:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	log.Info("starting", "version", version, "base_url", cfg.BaseURL.String())

	if cfg.SessionSecretGenerated {
		log.Warn("no session secret configured, generated a temporary one; " +
			"set WFM_SESSION_SECRET so login forms and in-flight logins survive a restart")
	}
	if !cfg.CookieSecure {
		log.Warn("cookies are not marked Secure; only do this for local development")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := prepareDataDir(cfg.DataDir); err != nil {
		return err
	}

	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Error("closing database", "error", err)
		}
	}()

	if err := applySeed(ctx, cfg, st, log); err != nil {
		return err
	}

	bundle, err := i18n.Load(log)
	if err != nil {
		return err
	}

	// Discovery is a network call. Doing it here means an unreachable provider
	// fails the deployment loudly instead of failing the first person to sign in.
	oidcClient, err := auth.NewOIDC(ctx, cfg)
	if err != nil {
		return err
	}

	sessions := auth.NewManager(st, auth.Config{
		SessionTTL:   cfg.SessionTTL,
		CookieSecure: cfg.CookieSecure,
		Secret:       cfg.SessionSecret,
	}, log)

	accounts := users.NewService(st, log)
	if err := accounts.Bootstrap(ctx, cfg); err != nil {
		return err
	}

	srv, err := web.New(web.Options{
		Config:   cfg,
		Store:    st,
		Users:    accounts,
		Sessions: sessions,
		OIDC:     oidcClient,
		Bundle:   bundle,
		Logger:   log,
		Version:  version,
	})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	go housekeeping(ctx, st, log)

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return nil
}

// prepareDataDir creates the directory tree on the PersistentVolume. Failing here
// early gives a clear message instead of a confusing SQLite error later.
func prepareDataDir(dataDir string) error {
	for _, dir := range []string{
		dataDir,
		filepath.Join(dataDir, "media", "original"),
		filepath.Join(dataDir, "media", "thumb"),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("prepare data directory %s: %w", dir, err)
		}
	}
	return nil
}

// applySeed loads the content seed rendered by the Helm chart and applies it
// according to the configured mode.
func applySeed(ctx context.Context, cfg *config.Config, st *store.Store, log *slog.Logger) error {
	if cfg.SeedMode == config.SeedNever || cfg.SeedFile == "" {
		return nil
	}

	seed, err := store.LoadSeedFile(cfg.SeedFile)
	if err != nil {
		return err
	}
	if seed == nil {
		log.Info("no seed file present, skipping", "path", cfg.SeedFile)
		return nil
	}

	if cfg.SeedMode == config.SeedOnce {
		applied, err := st.SeedApplied(ctx)
		if err != nil {
			return err
		}
		if applied {
			log.Debug("seed already applied, leaving the database alone")
			return nil
		}
	}

	if err := st.ApplySeed(ctx, seed); err != nil {
		return fmt.Errorf("apply content seed: %w", err)
	}
	log.Info("applied content seed", "mode", string(cfg.SeedMode), "path", cfg.SeedFile)
	return nil
}

// housekeeping sweeps expired sessions and invites until the context is cancelled.
func housekeeping(ctx context.Context, st *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(housekeepingInterval)
	defer ticker.Stop()

	sweep := func() {
		sessions, err := st.DeleteExpiredSessions(ctx)
		if err != nil {
			log.Error("sweeping expired sessions", "error", err)
		}
		invites, err := st.DeleteExpiredInvites(ctx)
		if err != nil {
			log.Error("sweeping expired invites", "error", err)
		}
		if sessions > 0 || invites > 0 {
			log.Info("housekeeping", "sessions_removed", sessions, "invites_removed", invites)
		}
	}

	sweep()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
