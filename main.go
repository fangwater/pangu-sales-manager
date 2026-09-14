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
	var marketingObserver *marketing.ActivityObserver
	if config.MarketingEnabled {
		client := temu.NewClient(config.MarketingAPIBaseURL, config.MarketingAppKey, config.MarketingAppSecret, config.MarketingAccessToken, config.MarketingRequestTimeout)
		if err := client.SetRequestInterval(config.MarketingRequestInterval); err != nil {
			logger.Error("configure Temu marketing client", "error", err)
			os.Exit(1)
		}
		marketingSyncer = marketing.NewSyncer(client)
		marketingObserver = marketing.NewActivityObserver()
	}

	if *syncOnce {
		counts, err := syncer.Run(ctx)
		if err != nil {
			logger.Error("sync failed", "error", err, "counts", counts)
			os.Exit(1)
		}
		return
	}

	api, err := newAPIServer(store, syncer, marketingSyncer, marketingObserver, config.BusinessTimezone, logger)
	if err != nil {
		logger.Error("initialize HTTP server", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr: config.ListenAddr, Handler: api.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 3 * time.Minute,
		WriteTimeout: 5 * time.Minute, IdleTimeout: 90 * time.Second,
	}

	go scheduleSync(ctx, syncer, config.SyncInterval, logger)
	if marketingSyncer != nil {
		go scheduleMarketingSync(ctx, marketingSyncer, marketingObserver, store, config.MarketingSyncInterval, config.MarketingRequestTimeout*4, logger)
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

func scheduleMarketingSync(ctx context.Context, syncer *marketing.Syncer, observer *marketing.ActivityObserver, store *Store, interval, timeout time.Duration, logger *slog.Logger) {
	run := func() {
		syncCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		snapshot, err := syncer.Sync(syncCtx)
		if err != nil {
			logger.Warn("Temu current activity price sync failed", "error", err, "enrollments", len(snapshot.Enrollments), "pages", snapshot.EnrollmentPages)
			return
		}
		observation := observer.Observe(snapshot)
		err = store.updateTemuSKUPriceIntervals(syncCtx, snapshot.CompletedAt, observation.SKUPrices)
		if err != nil {
			logger.Error("Temu SKU price interval persistence failed", "error", err, "prices", len(observation.SKUPrices))
			return
		}
		logger.Info("Temu current activity price sync completed", "enrollments", len(snapshot.Enrollments), "enrollment_pages", snapshot.EnrollmentPages, "goods_skcs", len(snapshot.GoodsBySKC), "goods_pages", snapshot.GoodsPages, "sku_prices", len(observation.SKUPrices))
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
	consecutiveFailures := 0
	for {
		syncCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		_, err := syncer.Run(syncCtx)
		cancel()

		delay := interval
		if err != nil && !errors.Is(err, errSyncRunning) {
			var deferred *syncDeferredError
			if errors.As(err, &deferred) {
				delay = maxDuration(interval, deferred.RetryAfter)
				logger.Warn("scheduled sync deferred", "error", err, "retry_in", delay)
			} else {
				consecutiveFailures++
				delay = syncRetryDelay(interval, consecutiveFailures)
				logger.Error("scheduled sync failed", "error", err, "retry_in", delay)
			}
		} else {
			consecutiveFailures = 0
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func syncRetryDelay(interval time.Duration, consecutiveFailures int) time.Duration {
	maxDelay := 30 * time.Minute
	if interval > maxDelay {
		maxDelay = interval
	}
	delay := interval
	for i := 0; i < consecutiveFailures && delay < maxDelay; i++ {
		if delay > maxDelay/2 {
			return maxDelay
		}
		delay *= 2
	}
	return minDuration(delay, maxDelay)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
