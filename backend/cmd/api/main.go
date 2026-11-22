package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"energytool/backend/internal/consultation"
	"energytool/backend/internal/httpapi"
	"energytool/backend/internal/service"
	"energytool/backend/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	addr := env("BACKEND_ADDR", ":8080")
	storePath := env("BACKEND_STORE_PATH", "./var/sessions.json")
	snapshotPath := env("EXPERT_KB_SNAPSHOT", "./var/expert-kb.json")
	operatorToken := strings.TrimSpace(os.Getenv("OPERATOR_TOKEN"))
	store, err := store.NewFileStore(storePath)
	if err != nil {
		logger.Error("store initialization failed", "error", err)
		os.Exit(1)
	}

	var ml consultation.Client
	mlURL := strings.TrimSpace(os.Getenv("ML_SERVICE_URL"))
	if mlURL != "" {
		timeout, err := time.ParseDuration(env("ML_TIMEOUT", "250s"))
		if err != nil {
			logger.Error("invalid ML_TIMEOUT", "error", err)
			os.Exit(1)
		}
		client, err := consultation.NewHTTPClient(mlURL, timeout)
		if err != nil {
			logger.Error("invalid ML configuration", "error", err)
			os.Exit(1)
		}
		ml = client
	} else {
		logger.Warn("ML_SERVICE_URL is unset; consultation requests will return a technical fallback")
	}

	app := service.New(store, ml)
	app.SetSnapshotPath(snapshotPath)
	app.SetDemoMode(os.Getenv("DEMO_MODE") == "true")
	ready := func(ctx context.Context) error {
		client, ok := ml.(interface{ Ready(context.Context) error })
		if !ok {
			return errors.New("consultation service is not configured")
		}
		return client.Ready(ctx)
	}
	api := httpapi.New(app, ready, logger, 255*time.Second, operatorToken)
	server := &http.Server{Addr: addr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 270 * time.Second, IdleTimeout: 60 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = app.RetryPendingExpertGaps()
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("backend listening", "address", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("backend stopped", "error", err)
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
