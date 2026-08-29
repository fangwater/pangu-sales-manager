package main

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type AnalyticsFilter struct {
	Period    string
	Platform  string
	Shop      string
	Warehouse string
}

type DashboardResponse struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Period      string             `json:"period"`
	Range       DateRange          `json:"range"`
	Summary     DashboardSummary   `json:"summary"`
	Series      []SeriesPoint      `json:"series"`
	SKUs        []SKUPerformance   `json:"skus"`
	Platforms   []Breakdown        `json:"platforms"`
	Warehouses  []WarehouseSummary `json:"warehouses"`
	Mapping     MappingQuality     `json:"mapping_quality"`
	Sync        SyncStatus         `json:"sync"`
}

type DateRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type DashboardSummary struct {
	Orders          int64   `json:"orders"`
	PlatformUnits   float64 `json:"platform_units"`
	WarehouseUnits  float64 `json:"warehouse_units"`
	ActiveSKUs      int     `json:"active_skus"`
	AvailableStock  float64 `json:"available_stock"`
	PeriodGrowthPct float64 `json:"period_growth_pct"`
}

type SeriesPoint struct {
	Bucket         string  `json:"bucket"`
	Label          string  `json:"label"`
	Orders         int64   `json:"orders"`
	PlatformUnits  float64 `json:"platform_units"`
	WarehouseUnits float64 `json:"warehouse_units"`
}

type Forecast struct {
	DailyRunRate float64 `json:"daily_run_rate"`
	Next7Days    float64 `json:"next_7_days"`
	Next30Days   float64 `json:"next_30_days"`
	TrendPct     float64 `json:"trend_pct"`
	Confidence   string  `json:"confidence"`
	HistoryDays  int     `json:"history_days"`
	Method       string  `json:"method"`
}

type SKUPerformance struct {
	WarehouseSKU   string   `json:"warehouse_sku"`
	ProductName    string   `json:"product_name"`
	PlatformUnits  float64  `json:"platform_units"`
	WarehouseUnits float64  `json:"warehouse_units"`
	PriorUnits     float64  `json:"prior_units"`
	GrowthPct      float64  `json:"growth_pct"`
	AvailableStock float64  `json:"available_stock"`
	DaysOfCover    *float64 `json:"days_of_cover"`
	WarehouseCount int      `json:"warehouse_count"`
	OrderCount     int64    `json:"order_count"`
	Forecast       Forecast `json:"forecast"`
}

type Breakdown struct {
	Key            string  `json:"key"`
	Label          string  `json:"label"`
	WarehouseUnits float64 `json:"warehouse_units"`
	SharePct       float64 `json:"share_pct"`
}

type WarehouseSummary struct {
	Code           string  `json:"code"`
	Name           string  `json:"name"`
	AvailableStock float64 `json:"available_stock"`
	WarehouseUnits float64 `json:"warehouse_units"`
	ActiveSKUCount int     `json:"active_sku_count"`
}

type MappingQuality struct {
	Total       int     `json:"total"`
	Verified    int     `json:"verified"`
	Inferred    int     `json:"inferred"`
	Unmapped    int     `json:"unmapped"`
	CoveragePct float64 `json:"coverage_pct"`
}

type SyncStatus struct {
	ID              int64      `json:"id"`
	Status          string     `json:"status"`
	Mode            string     `json:"mode"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	OrdersSynced    int        `json:"orders_synced"`
	LinesSynced     int        `json:"lines_synced"`
	InventorySynced int        `json:"inventory_synced"`
	Error           string     `json:"error,omitempty"`
}

type dailySKU struct {
	Date           time.Time
	SKU            string
	ProductName    string
	Platform       string
	Warehouse      string
	PlatformUnits  float64
	WarehouseUnits float64
	Orders         int64
}

type dailyTotal struct {
	Date           time.Time
	Orders         int64
	PlatformUnits  float64
	WarehouseUnits float64
}

type inventoryTotal struct {
	SKU           string
	Warehouse     string
	WarehouseName string
	Available     float64
}

func (s *Store) dashboard(ctx context.Context, filter AnalyticsFilter, timezone string) (DashboardResponse, error) {
	period := filter.Period
	if period != "day" && period != "week" && period != "month" {
		period = "day"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return DashboardResponse{}, err
	}
	now := time.Now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	currentStart, previousStart := periodWindows(period, today)
	end := today.AddDate(0, 0, 1)
	forecastStart := today.AddDate(0, 0, -89)
	queryStart := previousStart
	if forecastStart.Before(queryStart) {
		queryStart = forecastStart
	}

	dailyRows, err := s.queryDailySKUs(ctx, timezone, queryStart, end, filter)
	if err != nil {
		return DashboardResponse{}, err
	}
	totals, err := s.queryDailyTotals(ctx, timezone, queryStart, end, filter)
	if err != nil {
		return DashboardResponse{}, err
	}
	inventory, err := s.queryInventoryTotals(ctx, filter.Warehouse)
	if err != nil {
		return DashboardResponse{}, err
	}
	mapping, err := s.queryMappingQuality(ctx, filter.Platform, filter.Shop)
	if err != nil {
		return DashboardResponse{}, err
	}
	syncStatus, err := s.latestSync(ctx)
	if err != nil {
		return DashboardResponse{}, err
	}

	response := buildDashboard(period, today, currentStart, previousStart, end, dailyRows, totals, inventory)
	response.GeneratedAt = now
	response.Mapping = mapping
	response.Sync = syncStatus
	return response, nil
}

func (s *Store) queryDailySKUs(ctx context.Context, timezone string, start, end time.Time, filter AnalyticsFilter) ([]dailySKU, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (o.occurred_at AT TIME ZONE $1)::date,
		       l.warehouse_sku, MAX(COALESCE(NULLIF(l.product_name,''), sku.product_name)),
		       o.platform, COALESCE(l.warehouse_code,''),
		       SUM(l.quantity), SUM(l.warehouse_quantity), COUNT(DISTINCT o.id)
		FROM normalized_orders o
		JOIN normalized_order_lines l ON l.order_id=o.id
		JOIN canonical_skus sku ON sku.warehouse_sku=l.warehouse_sku
		WHERE o.sales_eligible
		  AND o.occurred_at >= $2 AND o.occurred_at < $3
		  AND ($4='' OR o.platform=$4)
		  AND ($5='' OR o.shop_key=$5)
		  AND ($6='' OR l.warehouse_code=$6)
		GROUP BY 1,2,4,5
		ORDER BY 1,2
	`, timezone, start, end, filter.Platform, filter.Shop, filter.Warehouse)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []dailySKU
	for rows.Next() {
		var row dailySKU
		if err := rows.Scan(&row.Date, &row.SKU, &row.ProductName, &row.Platform,
			&row.Warehouse, &row.PlatformUnits, &row.WarehouseUnits, &row.Orders); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryDailyTotals(ctx context.Context, timezone string, start, end time.Time, filter AnalyticsFilter) ([]dailyTotal, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (o.occurred_at AT TIME ZONE $1)::date,
		       COUNT(DISTINCT o.id), SUM(l.quantity), SUM(l.warehouse_quantity)
		FROM normalized_orders o
		JOIN normalized_order_lines l ON l.order_id=o.id
		WHERE o.sales_eligible
		  AND o.occurred_at >= $2 AND o.occurred_at < $3
		  AND ($4='' OR o.platform=$4)
		  AND ($5='' OR o.shop_key=$5)
		  AND ($6='' OR l.warehouse_code=$6)
		GROUP BY 1 ORDER BY 1
	`, timezone, start, end, filter.Platform, filter.Shop, filter.Warehouse)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []dailyTotal
	for rows.Next() {
		var row dailyTotal
		if err := rows.Scan(&row.Date, &row.Orders, &row.PlatformUnits, &row.WarehouseUnits); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryInventoryTotals(ctx context.Context, warehouse string) ([]inventoryTotal, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.warehouse_sku, i.warehouse_code, COALESCE(w.name,i.warehouse_name), SUM(i.available_quantity)
		FROM warehouse_inventory i
		LEFT JOIN warehouses w ON w.code=i.warehouse_code
		WHERE ($1='' OR i.warehouse_code=$1)
		GROUP BY 1,2,3 ORDER BY 2,1
	`, warehouse)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []inventoryTotal
	for rows.Next() {
		var row inventoryTotal
		if err := rows.Scan(&row.SKU, &row.Warehouse, &row.WarehouseName, &row.Available); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryMappingQuality(ctx context.Context, platform, shop string) (MappingQuality, error) {
	var quality MappingQuality
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE mapping_status IN ('mapped','identity','manual')),
		       COUNT(*) FILTER (WHERE mapping_status='inferred'),
		       COUNT(*) FILTER (WHERE mapping_status='unmapped')
		FROM sku_mappings
		WHERE ($1='' OR platform=$1) AND ($2='' OR shop_key=$2)
	`, platform, shop).Scan(&quality.Total, &quality.Verified, &quality.Inferred, &quality.Unmapped)
	if err != nil {
		return quality, err
	}
	if quality.Total > 0 {
		quality.CoveragePct = round1(float64(quality.Verified) / float64(quality.Total) * 100)
	}
	return quality, nil
}

func (s *Store) latestSync(ctx context.Context) (SyncStatus, error) {
	var status SyncStatus
	var started, completed sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id,status,sync_mode,started_at,completed_at,orders_synced,lines_synced,inventory_synced,error_message
		FROM sync_runs ORDER BY id DESC LIMIT 1
	`).Scan(&status.ID, &status.Status, &status.Mode, &started, &completed, &status.OrdersSynced,
		&status.LinesSynced, &status.InventorySynced, &status.Error)
	if err == sql.ErrNoRows {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	if started.Valid {
		status.StartedAt = &started.Time
	}
	if completed.Valid {
		status.CompletedAt = &completed.Time
	}
	return status, nil
}

func buildDashboard(period string, today, currentStart, previousStart, end time.Time, rows []dailySKU, totals []dailyTotal, inventory []inventoryTotal) DashboardResponse {
	response := DashboardResponse{
		Period: period,
		Range:  DateRange{Start: currentStart.Format("2006-01-02"), End: today.Format("2006-01-02")},
	}

	type skuAccumulator struct {
		name          string
		platformUnits float64
		current       float64
		previous      float64
		orders        int64
		daily         map[string]float64
	}
	skus := make(map[string]*skuAccumulator)
	platforms := make(map[string]float64)
	warehouseSales := make(map[string]float64)
	activeWarehouses := make(map[string]map[string]struct{})

	for _, row := range rows {
		item := skus[row.SKU]
		if item == nil {
			item = &skuAccumulator{daily: make(map[string]float64)}
			skus[row.SKU] = item
		}
		if item.name == "" && row.ProductName != "" {
			item.name = row.ProductName
		}
		dayKey := row.Date.Format("2006-01-02")
		item.daily[dayKey] += row.WarehouseUnits
		if !row.Date.Before(currentStart) {
			item.current += row.WarehouseUnits
			item.platformUnits += row.PlatformUnits
			item.orders += row.Orders
			platforms[row.Platform] += row.WarehouseUnits
			warehouseSales[row.Warehouse] += row.WarehouseUnits
		} else if !row.Date.Before(previousStart) {
			item.previous += row.WarehouseUnits
		}
	}

	stockBySKU := make(map[string]float64)
	warehouseNames := make(map[string]string)
	warehouseStock := make(map[string]float64)
	for _, row := range inventory {
		stockBySKU[row.SKU] += row.Available
		warehouseStock[row.Warehouse] += row.Available
		warehouseNames[row.Warehouse] = row.WarehouseName
		if activeWarehouses[row.Warehouse] == nil {
			activeWarehouses[row.Warehouse] = make(map[string]struct{})
		}
		if row.Available != 0 {
			activeWarehouses[row.Warehouse][row.SKU] = struct{}{}
		}
		response.Summary.AvailableStock += row.Available
	}

	forecastStart := today.AddDate(0, 0, -89)
	for sku, item := range skus {
		if item.current <= 0 {
			continue
		}
		series := make([]float64, 90)
		for i := range series {
			date := forecastStart.AddDate(0, 0, i).Format("2006-01-02")
			series[i] = item.daily[date]
		}
		prediction := forecastDemand(series)
		stock := stockBySKU[sku]
		var daysOfCover *float64
		if prediction.DailyRunRate > 0 {
			cover := round1(stock / prediction.DailyRunRate)
			daysOfCover = &cover
		}
		warehouseCount := 0
		for _, skuSet := range activeWarehouses {
			if _, ok := skuSet[sku]; ok {
				warehouseCount++
			}
		}
		response.SKUs = append(response.SKUs, SKUPerformance{
			WarehouseSKU: sku, ProductName: item.name,
			PlatformUnits: round1(item.platformUnits), WarehouseUnits: round1(item.current),
			PriorUnits: round1(item.previous), GrowthPct: percentChange(item.current, item.previous),
			AvailableStock: round1(stock), DaysOfCover: daysOfCover,
			WarehouseCount: warehouseCount, OrderCount: item.orders, Forecast: prediction,
		})
		response.Summary.PlatformUnits += item.platformUnits
		response.Summary.WarehouseUnits += item.current
	}
	sort.Slice(response.SKUs, func(i, j int) bool {
		return response.SKUs[i].WarehouseUnits > response.SKUs[j].WarehouseUnits
	})
	response.Summary.ActiveSKUs = len(response.SKUs)

	currentTotal := 0.0
	previousTotal := 0.0
	seriesMap := make(map[string]*SeriesPoint)
	for bucket := bucketStart(period, currentStart); bucket.Before(end); bucket = nextBucket(period, bucket) {
		key := bucket.Format("2006-01-02")
		seriesMap[key] = &SeriesPoint{Bucket: key, Label: bucketLabel(period, bucket)}
	}
	for _, row := range totals {
		if !row.Date.Before(currentStart) {
			bucket := bucketStart(period, row.Date)
			if point := seriesMap[bucket.Format("2006-01-02")]; point != nil {
				point.Orders += row.Orders
				point.PlatformUnits += row.PlatformUnits
				point.WarehouseUnits += row.WarehouseUnits
			}
			response.Summary.Orders += row.Orders
			currentTotal += row.WarehouseUnits
		} else if !row.Date.Before(previousStart) {
			previousTotal += row.WarehouseUnits
		}
	}
	keys := make([]string, 0, len(seriesMap))
	for key := range seriesMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		point := seriesMap[key]
		point.PlatformUnits = round1(point.PlatformUnits)
		point.WarehouseUnits = round1(point.WarehouseUnits)
		response.Series = append(response.Series, *point)
	}
	response.Summary.PlatformUnits = round1(response.Summary.PlatformUnits)
	response.Summary.WarehouseUnits = round1(response.Summary.WarehouseUnits)
	response.Summary.AvailableStock = round1(response.Summary.AvailableStock)
	response.Summary.PeriodGrowthPct = percentChange(currentTotal, previousTotal)

	for key, value := range platforms {
		label := strings.ToUpper(key)
		response.Platforms = append(response.Platforms, Breakdown{
			Key: key, Label: label, WarehouseUnits: round1(value),
			SharePct: share(value, currentTotal),
		})
	}
	sort.Slice(response.Platforms, func(i, j int) bool {
		return response.Platforms[i].WarehouseUnits > response.Platforms[j].WarehouseUnits
	})

	warehouseCodes := make(map[string]struct{})
	for code := range warehouseStock {
		warehouseCodes[code] = struct{}{}
	}
	for code := range warehouseSales {
		warehouseCodes[code] = struct{}{}
	}
	for code := range warehouseCodes {
		if code == "" {
			continue
		}
		response.Warehouses = append(response.Warehouses, WarehouseSummary{
			Code: code, Name: warehouseNames[code], AvailableStock: round1(warehouseStock[code]),
			WarehouseUnits: round1(warehouseSales[code]), ActiveSKUCount: len(activeWarehouses[code]),
		})
	}
	sort.Slice(response.Warehouses, func(i, j int) bool {
		return response.Warehouses[i].AvailableStock > response.Warehouses[j].AvailableStock
	})
	return response
}

func periodWindows(period string, today time.Time) (time.Time, time.Time) {
	switch period {
	case "week":
		start := monday(today).AddDate(0, 0, -11*7)
		return start, start.AddDate(0, 0, -12*7)
	case "month":
		start := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location()).AddDate(0, -5, 0)
		return start, start.AddDate(0, -6, 0)
	default:
		start := today.AddDate(0, 0, -13)
		return start, start.AddDate(0, 0, -14)
	}
}

func bucketStart(period string, date time.Time) time.Time {
	switch period {
	case "week":
		return monday(date)
	case "month":
		return time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, date.Location())
	default:
		return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	}
}

func nextBucket(period string, date time.Time) time.Time {
	if period == "month" {
		return date.AddDate(0, 1, 0)
	}
	if period == "week" {
		return date.AddDate(0, 0, 7)
	}
	return date.AddDate(0, 0, 1)
}

func bucketLabel(period string, date time.Time) string {
	if period == "month" {
		return date.Format("2006-01")
	}
	if period == "week" {
		return fmt.Sprintf("%02d周", isoWeek(date))
	}
	return date.Format("01-02")
}

func isoWeek(date time.Time) int {
	_, week := date.ISOWeek()
	return week
}

func monday(date time.Time) time.Time {
	weekday := (int(date.Weekday()) + 6) % 7
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location()).AddDate(0, 0, -weekday)
}

func forecastDemand(series []float64) Forecast {
	if len(series) == 0 {
		return Forecast{Confidence: "insufficient", Method: "weighted_moving_average_trend"}
	}
	last := func(days int) []float64 {
		if days > len(series) {
			days = len(series)
		}
		return series[len(series)-days:]
	}
	avg7 := average(last(7))
	avg28 := average(last(28))
	base := 0.65*avg7 + 0.35*avg28
	trendSeries := last(28)
	slope := linearSlope(trendSeries)
	capValue := math.Max(math.Max(avg7, avg28)*3, 1)
	forecastSum := func(days int) float64 {
		total := 0.0
		for day := 1; day <= days; day++ {
			value := base + slope*float64(day)
			value = math.Max(0, math.Min(value, capValue))
			total += value
		}
		return round1(total)
	}
	firstPositive := len(series)
	activeDays := 0
	for index, value := range series {
		if value > 0 {
			activeDays++
			if firstPositive == len(series) {
				firstPositive = index
			}
		}
	}
	historyDays := 0
	if firstPositive < len(series) {
		historyDays = len(series) - firstPositive
	}
	confidence := "low"
	if historyDays >= 42 && activeDays >= 14 {
		confidence = "high"
	} else if historyDays >= 14 && activeDays >= 5 {
		confidence = "medium"
	} else if historyDays == 0 {
		confidence = "insufficient"
	}
	trendPct := 0.0
	if avg28 > 0 {
		trendPct = (avg7 - avg28) / avg28 * 100
	}
	return Forecast{
		DailyRunRate: round1(base), Next7Days: forecastSum(7), Next30Days: forecastSum(30),
		TrendPct: round1(trendPct), Confidence: confidence, HistoryDays: historyDays,
		Method: "weighted_moving_average_trend",
	}
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func linearSlope(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	n := float64(len(values))
	var sumX, sumY, sumXY, sumXX float64
	for index, value := range values {
		x := float64(index)
		sumX += x
		sumY += value
		sumXY += x * value
		sumXX += x * x
	}
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denominator
}

func percentChange(current, previous float64) float64 {
	if previous == 0 {
		if current > 0 {
			return 100
		}
		return 0
	}
	return round1((current - previous) / previous * 100)
}

func share(value, total float64) float64 {
	if total == 0 {
		return 0
	}
	return round1(value / total * 100)
}

func round1(value float64) float64 {
	return math.Round(value*10) / 10
}
