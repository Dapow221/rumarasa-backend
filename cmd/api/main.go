package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"rumarasa-backend/internal/auth"
	"rumarasa-backend/internal/config"
	"rumarasa-backend/internal/db"
	"rumarasa-backend/internal/httpapi"
	"rumarasa-backend/internal/mail"
	"rumarasa-backend/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	st := store.New(pool)
	if err := bootstrapAdmin(ctx, cfg, st); err != nil {
		return err
	}
	go cleanupLoop(ctx, st)

	var mailer mail.Sender
	if cfg.ResendAPIKey != "" {
		mailer = mail.NewResend(cfg.ResendAPIKey, cfg.MailFrom)
	} else {
		slog.Warn("RESEND_API_KEY not set: member emails are disabled")
	}
	api := httpapi.NewServer(cfg, st, mailer)

	server := &http.Server{
		Addr:              cfg.BindAddr + ":" + cfg.Port,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server listening", "port", cfg.Port, "env", cfg.Env)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		// Let in-flight emails finish before the deferred pool.Close.
		api.Wait()
		return nil
	}
}

// bootstrapAdmin creates the initial admin from env vars when the table is
// empty, so a fresh deployment is immediately usable.
func bootstrapAdmin(ctx context.Context, cfg *config.Config, st *store.Store) error {
	n, err := st.CountAdmins(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if cfg.AdminUsername == "" || len(cfg.AdminPassword) < 12 {
		slog.Warn("no admin account exists; set ADMIN_USERNAME and ADMIN_PASSWORD (min 12 chars) to bootstrap one")
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), auth.BcryptCost)
	if err != nil {
		return err
	}
	if err := st.CreateAdmin(ctx, cfg.AdminUsername, string(hash)); err != nil {
		return err
	}
	slog.Info("bootstrapped admin account", "username", cfg.AdminUsername)
	return nil
}

// cleanupLoop prunes dead refresh tokens hourly.
func cleanupLoop(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := st.CleanupRefreshTokens(ctx); err != nil {
				slog.Error("cleanup refresh tokens", "err", err)
			}
		}
	}
}
