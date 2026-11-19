package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"paymenthub/internal/hub"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err.Error())
		os.Exit(1)
	}
}
func run() error {
	dsn, admin := os.Getenv("DATABASE_URL"), os.Getenv("ADMIN_SECRET")
	if dsn == "" || len(admin) < 32 {
		return fmt.Errorf("DATABASE_URL and ADMIN_SECRET (at least 32 characters) are required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	store, err := hub.Open(startup, dsn)
	if err != nil {
		return fmt.Errorf("database connection failed")
	}
	defer store.DB.Close()
	if err = store.Migrate(startup); err != nil {
		return fmt.Errorf("database migration failed: %w", err)
	}
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		slog.Info("migrations applied")
		return nil
	}
	vault, err := hub.NewVault(store, os.Getenv("ENCRYPTION_KEY"))
	if err != nil {
		return err
	}
	api := &hub.API{Store: store, Accounts: vault, Vault: vault, AdminSecret: admin}
	if base := os.Getenv("PUBLIC_WEBHOOK_BASE_URL"); base != "" {
		api.PublicWebhookBaseURL = base
	} else if host := os.Getenv("PUBLIC_WEBHOOK_HOSTNAME"); host != "" {
		api.PublicWebhookBaseURL = "https://" + host
	}
	if tenant := os.Getenv("WEBHOOK_DIAGNOSTIC_TENANT"); tenant != "" {
		until, parseErr := time.Parse(time.RFC3339, os.Getenv("WEBHOOK_DIAGNOSTIC_UNTIL"))
		if parseErr != nil || len(tenant) != 64 || until.After(time.Now().Add(15*time.Minute)) {
			return fmt.Errorf("invalid webhook diagnostic scope or expiry")
		}
		api.WebhookDiagnosticTenant = tenant
		api.WebhookDiagnosticUntil = until
	}
	worker := &hub.Worker{Store: store, Accounts: vault, Gateway: hub.NewGateway()}
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = ":8080"
	}
	server := &http.Server{Addr: address, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		dispatcher := &hub.NotificationDispatcher{Store: store, Vault: vault}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if e := dispatcher.DispatchNotifications(ctx); e != nil && ctx.Err() == nil {
					slog.Error("notification delivery failed", "error", e)
				}
			}
		}
	}()
	wg.Add(1)
	go func() { defer wg.Done(); worker.Run(ctx) }()
	errs := make(chan error, 1)
	go func() { slog.Info("payment hub listening", "address", address); errs <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err = <-errs:
		stop()
	}
	shutdown, done := context.WithTimeout(context.Background(), 30*time.Second)
	defer done()
	shutdownErr := server.Shutdown(shutdown)
	wg.Wait()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return shutdownErr
}
