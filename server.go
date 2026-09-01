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
	"sort"
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
	observer  *marketing.ActivityObserver
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

func newAPIServer(store *Store, syncer *Syncer, marketingSyncer *marketing.Syncer, observer *marketing.ActivityObserver, timezone string, logger *slog.Logger) (*APIServer, error) {
	static, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, err
	}
	return &APIServer{store: store, syncer: syncer, marketing: marketingSyncer, observer: observer, timezone: timezone, logger: logger, static: static}, nil
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
	mux.HandleFunc("GET /api/marketing/activity-snapshot", s.activitySnapshotRows)
	mux.HandleFunc("GET /api/marketing/skc-activity-states", s.skcActivityStates)
	mux.HandleFunc("GET /api/marketing/sku-price-snapshot", s.skuPriceSnapshot)
	mux.HandleFunc("GET /api/marketing/sku-price-snapshot/{skuID}/history", s.skuPriceSnapshotHistory)
	mux.HandleFunc("GET /api/marketing/sku-current-price", s.skuCurrentPrice)
	mux.HandleFunc("POST /api/marketing/sku-prices/query", s.querySKUPrices)
	mux.HandleFunc("POST /api/marketing/order-price-estimates/backfill", s.backfillOrderPriceEstimates)
	mux.HandleFunc("GET /api/profit/summary", s.profitSummary)
	mux.HandleFunc("POST /api/profit/import", s.importProfit)
	mux.HandleFunc("GET /", s.serveStatic)
	return s.middleware(mux)
}

func (s *APIServer) activitySnapshotRows(writer http.ResponseWriter, request *http.Request) {
	if s.marketing == nil {
		writeJSON(writer, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "活动价格同步未启用"})
		return
	}
	filter, err := activityRowFilter(request)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: err.Error()})
		return
	}
	items, summary, snapshot, err := s.marketing.ActivityRows(filter)
	if err != nil {
		if errors.Is(err, marketing.ErrSnapshotUnavailable) {
			writeJSON(writer, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "活动价格首次同步尚未完成"})
			return
		}
		s.internalError(writer, "query activity snapshot", err)
		return
	}
	stateCounts := map[string]int{}
	if s.observer != nil {
		view := s.observer.Latest()
		for index := range items {
			observation := view.Enrollments[marketing.EnrollmentObservationKey{EnrollID: items[index].EnrollID, SKCID: items[index].SKCID}]
			items[index].PreviousRemainingActivityStock = observation.PreviousRemainingActivityStock
			items[index].IntervalConsumedStock = observation.IntervalConsumedStock
			items[index].IntervalIncreasedStock = observation.IntervalIncreasedStock
			if state, ok := view.States[items[index].SKCID]; ok {
				items[index].SKCActivityStatus = state.Status
				items[index].SKCActiveEnrollID = state.ActiveEnrollID
				items[index].SKCPreviousActiveEnrollID = state.PreviousActiveEnrollID
				items[index].SKCEvidenceEnrollIDs = append([]int64(nil), state.EvidenceEnrollIDs...)
				items[index].SKCStateStartedAt = state.StateStartedAt
				items[index].SKCLastEvidenceAt = state.LastEvidenceAt
				items[index].SKCStateCarriedForward = state.CarriedForward
				items[index].SKCStateReason = state.Reason
				items[index].SelectedEffectiveActivity = state.Status == marketing.SKCActivityConfirmed && state.ActiveEnrollID == items[index].EnrollID
			}
		}
		for _, state := range view.States {
			stateCounts[state.Status]++
		}
	}
	page := normalizedPage(queryInt(request, "page", 1), 1, 100000)
	pageSize := normalizedPage(queryInt(request, "page_size", 1000), 1000, 1000)
	start := min((page-1)*pageSize, len(items))
	end := min(start+pageSize, len(items))
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: items[start:end], Meta: map[string]any{
		"page": page, "page_size": pageSize, "total": len(items), "synced_at": snapshot.CompletedAt,
		"started_at": snapshot.StartedAt, "summary": summary, "state_counts": stateCounts,
	}})
}

func (s *APIServer) skcActivityStates(writer http.ResponseWriter, request *http.Request) {
	if s.observer == nil {
		writeJSON(writer, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "活动状态观测未启用"})
		return
	}
	latest := s.observer.Latest()
	states := make([]marketing.SKCActivityState, 0, len(latest.States))
	for _, state := range latest.States {
		states = append(states, state)
	}
	sort.Slice(states, func(left, right int) bool { return states[left].SKCID < states[right].SKCID })
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: states, Meta: map[string]any{"total": len(states)}})
}

func (s *APIServer) skuPriceSnapshot(writer http.ResponseWriter, request *http.Request) {
	filter := struct {
		SKUID  int64
		SKCID  int64
		Status string
	}{Status: strings.TrimSpace(request.URL.Query().Get("status"))}
	var err error
	if filter.SKUID, err = positiveQueryID(request, "sku_id"); err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: err.Error()})
		return
	}
	if filter.SKCID, err = positiveQueryID(request, "skc_id"); err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: err.Error()})
		return
	}
	if filter.Status != "" && filter.Status != marketing.SKCActivityConfirmed && filter.Status != marketing.SKCActivityWarning {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "status must be confirmed or warning"})
		return
	}
	states, err := s.store.latestSKUPriceStates(request.Context(), filter.SKUID, filter.SKCID, filter.Status)
	if err != nil {
		s.internalError(writer, "load SKU price snapshot", err)
		return
	}
	counts := map[string]int{}
	for _, state := range states {
		counts[state.Status]++
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: states, Meta: map[string]any{"total": len(states), "status_counts": counts}})
}

func (s *APIServer) skuPriceSnapshotHistory(writer http.ResponseWriter, request *http.Request) {
	skuID, err := strconv.ParseInt(request.PathValue("skuID"), 10, 64)
	if err != nil || skuID <= 0 {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "skuID must be a positive integer"})
		return
	}
	limit := normalizedPage(queryInt(request, "limit", 120), 120, 1440)
	states, err := s.store.skuPriceStateHistory(request.Context(), skuID, limit)
	if err != nil {
		s.internalError(writer, "load SKU price history", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: states, Meta: map[string]any{"total": len(states), "sku_id": skuID}})
}

func (s *APIServer) skuCurrentPrice(writer http.ResponseWriter, request *http.Request) {
	if s.observer == nil {
		writeJSON(writer, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "SKU 价格观测未启用"})
		return
	}
	skuID, err := positiveQueryID(request, "sku_id")
	if err != nil || skuID == 0 {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "sku_id must be a positive integer"})
		return
	}
	results, err := resolveSKUPriceQueries(request.Context(), s.observer, s.store, []SKUPriceQueryItem{{SKUID: skuID}})
	if err != nil {
		s.internalError(writer, "query current SKU price", err)
		return
	}
	if len(results) == 0 || results[0].Reason == "current_sku_not_found" {
		writeJSON(writer, http.StatusNotFound, apiResponse{Success: false, Error: "当前活动中没有该 SKU"})
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: results[0]})
}

func (s *APIServer) querySKUPrices(writer http.ResponseWriter, request *http.Request) {
	if s.observer == nil {
		writeJSON(writer, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "SKU 价格观测未启用"})
		return
	}
	var payload SKUPriceQueryRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "invalid JSON body"})
		return
	}
	results, err := resolveSKUPriceQueries(request.Context(), s.observer, s.store, payload.Items)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: err.Error()})
		return
	}
	counts := map[string]int{}
	for _, result := range results {
		counts[result.Status]++
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: results, Meta: map[string]any{"total": len(results), "status_counts": counts}})
}

func (s *APIServer) backfillOrderPriceEstimates(writer http.ResponseWriter, request *http.Request) {
	if s.observer == nil {
		writeJSON(writer, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "SKU 价格观测未启用"})
		return
	}
	var payload OrderPriceBackfillRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "invalid JSON body"})
		return
	}
	stats, err := backfillOrderPriceEstimates(request.Context(), s.store, s.observer, payload)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: stats})
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
	var err error
	if filter.SKCID, err = positiveQueryID(request, "skc_id"); err != nil {
		return filter, err
	}
	if filter.SKUID, err = positiveQueryID(request, "sku_id"); err != nil {
		return filter, err
	}
	if filter.SiteID, err = positiveQueryID(request, "site_id"); err != nil {
		return filter, err
	}
	return filter, nil
}

func activityRowFilter(request *http.Request) (marketing.ActivityRowFilter, error) {
	filter := marketing.ActivityRowFilter{}
	values := []struct {
		name   string
		target *int64
	}{{"skc_id", &filter.SKCID}, {"sku_id", &filter.SKUID}, {"site_id", &filter.SiteID}, {"enroll_id", &filter.EnrollID}, {"activity_type", &filter.ActivityType}}
	for _, value := range values {
		parsed, err := positiveQueryID(request, value.name)
		if err != nil {
			return filter, err
		}
		*value.target = parsed
	}
	return filter, nil
}

func positiveQueryID(request *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(request.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, errors.New(name + " must be a positive integer")
	}
	return parsed, nil
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
