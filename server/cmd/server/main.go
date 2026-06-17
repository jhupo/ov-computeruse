package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ov-computeruse/server/internal/app"
	"ov-computeruse/server/internal/config"
	"ov-computeruse/server/internal/platform/logger"
	"ov-computeruse/server/internal/platform/postgres"
	"ov-computeruse/server/internal/platform/redisx"
	"ov-computeruse/server/internal/store"
)

func main() {
	log := logger.New("info")
	cfg, err := config.Load()
	fatalIf(log, err)
	log = logger.New(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	redisClient, err := redisx.Open(ctx, cfg.RedisURL)
	fatalIf(log, err)
	defer redisClient.Close()
	postgresPool, err := postgres.Open(ctx, cfg.PostgresURL)
	fatalIf(log, err)
	defer postgresPool.Close()
	st, err := store.New(ctx, postgresPool)
	fatalIf(log, err)
	server := app.New(cfg, st, redisClient, log)
	server.Run(ctx)
	if cfg.BindUsersJSON != "" {
		var users []store.BindUser
		fatalIf(log, json.Unmarshal([]byte(cfg.BindUsersJSON), &users))
		fatalIf(log, server.SeedUsers(ctx, users))
	}
	httpServer := &http.Server{Addr: cfg.Addr, Handler: server.Routes(), ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout}
	go func() {
		log.Info("server listening", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server failed", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

func fatalIf(logger *slog.Logger, err error) {
	if err == nil {
		return
	}
	logger.Error("server failed", "error", err)
	os.Exit(1)
}
