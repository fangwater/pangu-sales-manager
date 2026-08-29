package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	ListenAddr               string
	DatabaseURL              string
	SheinDatabaseURL         string
	TemuDatabaseURL          string
	XLWMSDatabaseURL         string
	SyncInterval             time.Duration
	BusinessTimezone         string
	MarketingEnabled         bool
	MarketingAPIBaseURL      string
	MarketingAccessToken     string
	MarketingAppKey          string
	MarketingAppSecret       string
	MarketingSyncInterval    time.Duration
	MarketingRequestInterval time.Duration
	MarketingRequestTimeout  time.Duration
}

func loadConfig() (Config, error) {
	marketingEnabled, err := strconv.ParseBool(envOr("TEMU_MARKETING_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("TEMU_MARKETING_ENABLED must be true or false: %w", err)
	}
	config := Config{
		ListenAddr:               envOr("LISTEN_ADDR", "127.0.0.1:18100"),
		DatabaseURL:              envOr("DATABASE_URL", "host=/var/run/postgresql dbname=pangu_sales user=fanghaizhou sslmode=disable"),
		SheinDatabaseURL:         envOr("SHEIN_DATABASE_URL", "host=pangutech.online port=5432 dbname=demo_app user=pangu_reader sslmode=require connect_timeout=10"),
		TemuDatabaseURL:          envOr("TEMU_DATABASE_URL", "host=pangutech.online port=5432 dbname=temu_manager user=pangu_reader sslmode=require connect_timeout=10"),
		XLWMSDatabaseURL:         envOr("XLWMS_DATABASE_URL", "host=pangutech.online port=5432 dbname=xlwms user=pangu_reader sslmode=require connect_timeout=10"),
		SyncInterval:             durationOr("SYNC_INTERVAL", time.Minute),
		BusinessTimezone:         envOr("BUSINESS_TIMEZONE", "Asia/Shanghai"),
		MarketingEnabled:         marketingEnabled,
		MarketingAPIBaseURL:      os.Getenv("TEMU_MARKETING_API_BASE_URL"),
		MarketingAccessToken:     os.Getenv("TEMU_MARKETING_ACCESS_TOKEN"),
		MarketingAppKey:          os.Getenv("TEMU_MARKETING_APP_KEY"),
		MarketingAppSecret:       os.Getenv("TEMU_MARKETING_APP_SECRET"),
		MarketingSyncInterval:    durationOr("TEMU_MARKETING_SYNC_INTERVAL", 30*time.Minute),
		MarketingRequestInterval: durationOr("TEMU_MARKETING_REQUEST_INTERVAL", 2*time.Second),
		MarketingRequestTimeout:  durationOr("TEMU_MARKETING_REQUEST_TIMEOUT", 30*time.Second),
	}
	if config.MarketingEnabled {
		for name, value := range map[string]string{
			"TEMU_MARKETING_API_BASE_URL": config.MarketingAPIBaseURL,
			"TEMU_MARKETING_ACCESS_TOKEN": config.MarketingAccessToken,
			"TEMU_MARKETING_APP_KEY":      config.MarketingAppKey,
			"TEMU_MARKETING_APP_SECRET":   config.MarketingAppSecret,
		} {
			if value == "" {
				return Config{}, fmt.Errorf("%s is required when TEMU_MARKETING_ENABLED=true", name)
			}
		}
		if config.MarketingRequestInterval > 10*time.Second {
			return Config{}, fmt.Errorf("TEMU_MARKETING_REQUEST_INTERVAL must not exceed 10s")
		}
	}
	return config, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func durationOr(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
