package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"pangu-sales-manager/internal/marketing"
	"pangu-sales-manager/internal/temu"
)

func main() {
	syncOnce := flag.Bool("sync-once", false, "synchronize all source data and exit")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	config, err := loadConfig()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := openStore(ctx, config.DatabaseURL)
	if err != nil {
		logger.Error("initialize store", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	syncer := newSyncer(config, store, logger)
	defer syncer.Close()
	var marketingSyncer *marketing.Syncer
	if config.MarketingEnabled {
		client := temu.NewClient(config.MarketingAPIBaseURL, config.MarketingAppKey, config.MarketingAppSecret, config.MarketingAccessToken, config.MarketingRequestTimeout)
		if err := client.SetRequestInterval(config.MarketingRequestInterval); err != nil {
			logger.Error("configure Temu marketing client", "error", err)
			os.Exit(1)
		}
		marketingSyncer = marketing.NewSyncer(client)
	}

	if *syncOnce {
		counts, err := syncer.Run(ctx)
		if err != nil {
			logger.Error("sync failed", "error", err, "counts", counts)
			os.Exit(1)
		}
		return
	}

	api, err := newAPIServer(store, syncer, marketingSyncer, config.BusinessTimezone, logger)
	if err != nil {
		logger.Error("initialize HTTP server", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr: config.ListenAddr, Handler: api.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second,
	}

	go scheduleSync(ctx, syncer, config.SyncInterval, logger)
	if marketingSyncer != nil {
		go scheduleMarketingSync(ctx, marketingSyncer, config.MarketingSyncInterval, config.MarketingRequestTimeout*4, logger)
	} else {
		logger.Info("Temu activity price sync disabled")
	}
	go func() {
		logger.Info("pangu sales manager listening", "address", config.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown HTTP server", "error", err)
	}
}

func scheduleMarketingSync(ctx context.Context, syncer *marketing.Syncer, interval, timeout time.Duration, logger *slog.Logger) {
	run := func() {
		syncCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		snapshot, err := syncer.Sync(syncCtx)
		if err != nil {
			logger.Warn("Temu activity price sync failed", "error", err, "activities", len(snapshot.Activities), "enrollments", len(snapshot.Enrollments), "pages", snapshot.EnrollmentPages)
			return
		}
		logger.Info("Temu activity price sync completed", "activities", len(snapshot.Activities), "enrollments", len(snapshot.Enrollments), "enrollment_pages", snapshot.EnrollmentPages, "goods_skcs", len(snapshot.GoodsBySKC), "goods_pages", snapshot.GoodsPages, "activity_details", len(snapshot.DetailsByType), "activity_detail_errors", len(snapshot.DetailErrors))
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func scheduleSync(ctx context.Context, syncer *Syncer, interval time.Duration, logger *slog.Logger) {
	run := func() {
		syncCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if _, err := syncer.Run(syncCtx); err != nil && !errors.Is(err, errSyncRunning) {
			logger.Error("scheduled sync failed", "error", err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
