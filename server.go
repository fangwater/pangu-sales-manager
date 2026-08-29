package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"pangu-sales-manager/internal/marketing"
)

//go:embed web/* web/vendor/*
var webFiles embed.FS

type APIServer struct {
	store     *Store
	syncer    *Syncer
	marketing *marketing.Syncer
	logger    *slog.Logger
	timezone  string
	static    fs.FS
}

type apiResponse struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	Meta    any    `json:"meta,omitempty"`
}

func newAPIServer(store *Store, syncer *Syncer, marketingSyncer *marketing.Syncer, timezone string, logger *slog.Logger) (*APIServer, error) {
	static, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, err
	}
	return &APIServer{store: store, syncer: syncer, marketing: marketingSyncer, timezone: timezone, logger: logger, static: static}, nil
}

func (s *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/dashboard", s.dashboard)
	mux.HandleFunc("GET /api/mappings", s.mappings)
	mux.HandleFunc("PATCH /api/mappings/{platform}/{shop}/{sku}", s.updateMapping)
	mux.HandleFunc("GET /api/orders", s.orders)
	mux.HandleFunc("GET /api/warehouses", s.warehouses)
	mux.HandleFunc("GET /api/sync/status", s.syncStatus)
	mux.HandleFunc("POST /api/sync", s.startSync)
	mux.HandleFunc("GET /api/marketing/effective-prices", s.effectiveActivityPrices)
	mux.HandleFunc("GET /", s.serveStatic)
	return s.middleware(mux)
}

func (s *APIServer) effectiveActivityPrices(writer http.ResponseWriter, request *http.Request) {
	if s.marketing == nil {
		writeJSON(writer, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "活动价格同步未启用"})
		return
	}
	filter, err := effectivePriceFilter(request)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: err.Error()})
		return
	}
	items, snapshot, err := s.marketing.EffectivePrices(filter)
	if err != nil {
		if errors.Is(err, marketing.ErrSnapshotUnavailable) {
			writeJSON(writer, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "活动价格首次同步尚未完成"})
			return
		}
		s.internalError(writer, "query activity prices", err)
		return
	}
	page := normalizedPage(queryInt(request, "page", 1), 1, 100000)
	pageSize := normalizedPage(queryInt(request, "page_size", 30), 30, 100)
	start := min((page-1)*pageSize, len(items))
	end := min(start+pageSize, len(items))
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: items[start:end], Meta: map[string]any{
		"page": page, "page_size": pageSize, "total": len(items), "synced_at": snapshot.CompletedAt,
	}})
}

func effectivePriceFilter(request *http.Request) (marketing.EffectivePriceFilter, error) {
	filter := marketing.EffectivePriceFilter{}
	values := []struct {
		name   string
		target *int64
	}{{"skc_id", &filter.SKCID}, {"sku_id", &filter.SKUID}, {"site_id", &filter.SiteID}}
	for _, value := range values {
		raw := strings.TrimSpace(request.URL.Query().Get(value.name))
		if raw == "" {
			continue
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			return filter, errors.New(value.name + " must be a positive integer")
		}
		*value.target = parsed
	}
	return filter, nil
}

func (s *APIServer) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "SAMEORIGIN")
		writer.Header().Set("Referrer-Policy", "same-origin")
		writer.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request)
		if strings.HasPrefix(request.URL.Path, "/api/") {
			s.logger.Info("http request", "method", request.Method, "path", request.URL.Path,
				"duration", time.Since(started).Round(time.Millisecond))
		}
	})
}

func (s *APIServer) health(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.db.PingContext(ctx); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "database unavailable"})
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"status": "ok", "service": "pangu-sales-manager", "sync_running": s.syncer.Running(),
	}})
}

func (s *APIServer) dashboard(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	filter := AnalyticsFilter{
		Period:    strings.TrimSpace(request.URL.Query().Get("period")),
		Platform:  strings.TrimSpace(request.URL.Query().Get("platform")),
		Shop:      strings.TrimSpace(request.URL.Query().Get("shop")),
		Warehouse: strings.TrimSpace(request.URL.Query().Get("warehouse")),
	}
	data, err := s.store.dashboard(ctx, filter, s.timezone)
	if err != nil {
		s.internalError(writer, "load dashboard", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: data})
}

func (s *APIServer) mappings(writer http.ResponseWriter, request *http.Request) {
	page := normalizedPage(queryInt(request, "page", 1), 1, 100000)
	size := normalizedPage(queryInt(request, "page_size", 30), 30, 100)
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	data, err := s.store.listMappings(ctx, request.URL.Query().Get("platform"),
		request.URL.Query().Get("shop"), request.URL.Query().Get("status"),
		request.URL.Query().Get("q"), page, size)
	if err != nil {
		s.internalError(writer, "list mappings", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: data})
}

func (s *APIServer) updateMapping(writer http.ResponseWriter, request *http.Request) {
	platform, err := url.PathUnescape(request.PathValue("platform"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "invalid platform"})
		return
	}
	shop, _ := url.PathUnescape(request.PathValue("shop"))
	platformSKU, _ := url.PathUnescape(request.PathValue("sku"))
	var payload struct {
		WarehouseSKU     string  `json:"warehouse_sku"`
		ConversionFactor float64 `json:"conversion_factor"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "invalid JSON body"})
		return
	}
	payload.WarehouseSKU = strings.TrimSpace(payload.WarehouseSKU)
	if payload.WarehouseSKU == "" || payload.ConversionFactor <= 0 {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "warehouse_sku and a positive conversion_factor are required"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	err = s.store.updateManualMapping(ctx, platform, shop, platformSKU, payload.WarehouseSKU, payload.ConversionFactor)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusNotFound, apiResponse{Success: false, Error: "mapping not found"})
		return
	}
	if err != nil {
		s.internalError(writer, "update mapping", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"updated": true}})
}

func (s *APIServer) orders(writer http.ResponseWriter, request *http.Request) {
	page := normalizedPage(queryInt(request, "page", 1), 1, 100000)
	size := normalizedPage(queryInt(request, "page_size", 30), 30, 100)
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	data, err := s.store.listOrders(ctx, request.URL.Query().Get("platform"),
		request.URL.Query().Get("shop"), request.URL.Query().Get("sku"), page, size)
	if err != nil {
		s.internalError(writer, "list normalized orders", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: data})
}

func (s *APIServer) warehouses(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	data, err := s.store.listWarehouses(ctx)
	if err != nil {
		s.internalError(writer, "list warehouses", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: data})
}

func (s *APIServer) syncStatus(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	status, err := s.store.latestSync(ctx)
	if err != nil {
		s.internalError(writer, "load sync status", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"run": status, "running": s.syncer.Running(),
	}})
}

func (s *APIServer) startSync(writer http.ResponseWriter, request *http.Request) {
	if s.syncer.Running() {
		writeJSON(writer, http.StatusConflict, apiResponse{Success: false, Error: "sync already running"})
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if _, err := s.syncer.Run(ctx); err != nil && !errors.Is(err, errSyncRunning) {
			s.logger.Error("manual sync failed", "error", err)
		}
	}()
	writeJSON(writer, http.StatusAccepted, apiResponse{Success: true, Data: map[string]bool{"started": true}})
}

func (s *APIServer) serveStatic(writer http.ResponseWriter, request *http.Request) {
	name := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	content, err := fs.ReadFile(s.static, name)
	if err != nil {
		content, err = fs.ReadFile(s.static, "index.html")
		name = "index.html"
	}
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType != "" {
		writer.Header().Set("Content-Type", contentType)
	}
	if strings.HasPrefix(name, "vendor/") {
		writer.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	}
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

func (s *APIServer) internalError(writer http.ResponseWriter, operation string, err error) {
	s.logger.Error(operation, "error", err)
	writeJSON(writer, http.StatusInternalServerError, apiResponse{Success: false, Error: operation})
}

func writeJSON(writer http.ResponseWriter, status int, payload apiResponse) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func queryInt(request *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(request.URL.Query().Get(key))
	if err != nil {
		return fallback
	}
	return value
}
