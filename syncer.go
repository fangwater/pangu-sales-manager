package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

var errSyncRunning = errors.New("a data sync is already running")

type SyncCounts struct {
	Orders    int `json:"orders"`
	Lines     int `json:"lines"`
	Inventory int `json:"inventory"`
}

type Syncer struct {
	store   *Store
	shein   *sql.DB
	temu    *sql.DB
	xlwms   *sql.DB
	logger  *slog.Logger
	running atomic.Bool
}

func newSyncer(config Config, store *Store, logger *slog.Logger) *Syncer {
	return &Syncer{
		store:  store,
		shein:  openSource(config.SheinDatabaseURL),
		temu:   openSource(config.TemuDatabaseURL),
		xlwms:  openSource(config.XLWMSDatabaseURL),
		logger: logger,
	}
}

func openSource(databaseURL string) *sql.DB {
	db, _ := sql.Open("postgres", databaseURL)
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(10 * time.Minute)
	return db
}

func (s *Syncer) Close() {
	s.shein.Close()
	s.temu.Close()
	s.xlwms.Close()
}

func (s *Syncer) Run(ctx context.Context) (counts SyncCounts, err error) {
	if !s.running.CompareAndSwap(false, true) {
		return counts, errSyncRunning
	}
	defer s.running.Store(false)

	plan, err := s.store.planSync(ctx)
	if err != nil {
		return counts, fmt.Errorf("plan sync: %w", err)
	}
	runID, err := s.store.beginSync(ctx, plan.Mode)
	if err != nil {
		return counts, err
	}
	started := time.Now()
	s.logger.Info("sales data sync started", "run_id", runID, "mode", plan.Mode)
	defer func() {
		status := "succeeded"
		if err != nil {
			status = "failed"
		}
		finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if finishErr := s.store.finishSync(finishCtx, runID, status, counts, err); finishErr != nil {
			s.logger.Error("finish sync run", "run_id", runID, "error", finishErr)
		}
		s.logger.Info("sales data sync completed", "run_id", runID, "status", status,
			"mode", plan.Mode, "orders", counts.Orders, "lines", counts.Lines, "inventory", counts.Inventory,
			"duration", time.Since(started).Round(time.Millisecond))
	}()

	warehouses, inventory, inventoryErr := s.readXLWMS(ctx)
	if inventoryErr == nil {
		counts.Inventory, inventoryErr = s.store.replaceInventory(ctx, warehouses, inventory)
	}

	sheinOrders, sheinLines, sheinErr := s.readShein(ctx, warehouses, inventory, plan.Since["shein\x00beauty-hangers-home"])
	if sheinErr == nil {
		var orders, lines int
		orders, lines, sheinErr = s.store.upsertSourceBatch(ctx, sheinOrders, sheinLines)
		counts.Orders += orders
		counts.Lines += lines
	}

	for _, shop := range []struct {
		key    string
		schema string
	}{{"panda-homes", "temu_panda_homes"}, {"panda-buy", "temu_panda_buy"}} {
		orders, lines, sourceErr := s.readTemu(ctx, shop.key, shop.schema, inventory, plan.Since["temu\x00"+shop.key])
		if sourceErr == nil {
			var orderCount, lineCount int
			orderCount, lineCount, sourceErr = s.store.upsertSourceBatch(ctx, orders, lines)
			counts.Orders += orderCount
			counts.Lines += lineCount
		}
		if sourceErr != nil {
			err = errors.Join(err, fmt.Errorf("sync Temu %s: %w", shop.key, sourceErr))
		}
	}

	if inventoryErr != nil {
		err = errors.Join(err, fmt.Errorf("sync XLWMS inventory: %w", inventoryErr))
	}
	if sheinErr != nil {
		err = errors.Join(err, fmt.Errorf("sync SHEIN: %w", sheinErr))
	}
	return counts, err
}

func (s *Syncer) Running() bool {
	return s.running.Load()
}

func (s *Syncer) readXLWMS(ctx context.Context) ([]Warehouse, []InventoryRow, error) {
	if err := s.xlwms.PingContext(ctx); err != nil {
		return nil, nil, err
	}
	warehouseRows, err := s.xlwms.QueryContext(ctx, `
		SELECT wh_code, warehouse_name, is_active
		FROM public.xlwms_warehouses
		ORDER BY wh_code
	`)
	if err != nil {
		return nil, nil, err
	}
	var warehouses []Warehouse
	for warehouseRows.Next() {
		var warehouse Warehouse
		if err := warehouseRows.Scan(&warehouse.Code, &warehouse.Name, &warehouse.Active); err != nil {
			warehouseRows.Close()
			return nil, nil, err
		}
		warehouses = append(warehouses, warehouse)
	}
	if err := warehouseRows.Close(); err != nil {
		return nil, nil, err
	}

	rows, err := s.xlwms.QueryContext(ctx, `
		SELECT wh_code, wh_name, sku,
		       COALESCE(total_amount, 0), COALESCE(available_amount, 0),
		       COALESCE(lock_amount, 0), COALESCE(transport_amount, 0),
		       statistic_date, last_seen_at
		FROM public.xlwms_inventory_records
		WHERE inventory_kind='integrated' AND BTRIM(sku) <> ''
		ORDER BY wh_code, sku
	`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var inventory []InventoryRow
	for rows.Next() {
		var row InventoryRow
		var statisticDate, updatedAt sql.NullTime
		if err := rows.Scan(&row.WarehouseCode, &row.WarehouseName, &row.WarehouseSKU,
			&row.Total, &row.Available, &row.Locked, &row.InTransit,
			&statisticDate, &updatedAt); err != nil {
			return nil, nil, err
		}
		if statisticDate.Valid {
			row.StatisticDate = &statisticDate.Time
		}
		if updatedAt.Valid {
			row.UpdatedAt = &updatedAt.Time
		}
		inventory = append(inventory, row)
	}
	return warehouses, inventory, rows.Err()
}

func (s *Syncer) readShein(ctx context.Context, warehouses []Warehouse, inventory []InventoryRow, since time.Time) ([]SourceOrder, []SourceLine, error) {
	if err := s.shein.PingContext(ctx); err != nil {
		return nil, nil, err
	}
	rows, err := s.shein.QueryContext(ctx, `
		SELECT o.order_no, COALESCE(o.order_status,''), COALESCE(o.order_status_normalized,''),
		       COALESCE(
		           NULLIF(o.detail_payload->>'orderTime','')::timestamptz,
		           NULLIF(o.order_created_at,'')::timestamp AT TIME ZONE 'Asia/Shanghai',
		           o.first_seen_at
		       ) AS occurred_at,
		       CASE WHEN NULLIF(o.detail_payload->>'orderTime','') IS NOT NULL THEN 'platform_order_time'
		            WHEN NULLIF(o.order_created_at,'') IS NOT NULL THEN 'list_order_time'
		            ELSE 'first_seen' END,
		       COALESCE(task.oms_warehouse_code,''),
		       COALESCE(o.order_status_normalized,'') <> 'refunded',
		       o.first_seen_at, o.last_seen_at,
		       COALESCE(o.detail_payload, o.list_payload, '{}'::jsonb)
		FROM shein_beauty_hangers_home.shein_orders o
		LEFT JOIN LATERAL (
			SELECT oms_warehouse_code
			FROM shein_beauty_hangers_home.shein_go_fulfillment_tasks task
			WHERE task.order_no=o.order_no AND BTRIM(COALESCE(task.oms_warehouse_code,'')) <> ''
			ORDER BY task.updated_at DESC LIMIT 1
		) task ON true
		WHERE ($1::timestamptz IS NULL OR o.last_seen_at >= $1)
		ORDER BY o.id
	`, nullableTime(since))
	if err != nil {
		return nil, nil, err
	}
	var orders []SourceOrder
	for rows.Next() {
		var order SourceOrder
		var raw []byte
		order.Platform = "shein"
		order.ShopKey = "beauty-hangers-home"
		if err := rows.Scan(&order.OrderNo, &order.SourceStatus, &order.NormalizedStatus,
			&order.OccurredAt, &order.OccurredAtSource, &order.WarehouseCode,
			&order.SalesEligible, &order.FirstSeenAt, &order.UpdatedAt, &raw); err != nil {
			rows.Close()
			return nil, nil, err
		}
		order.RawPayload = json.RawMessage(raw)
		orders = append(orders, order)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}

	lineRows, err := s.shein.QueryContext(ctx, `
		WITH expanded AS (
			SELECT o.order_no, item
			FROM shein_beauty_hangers_home.shein_orders o
			CROSS JOIN LATERAL jsonb_array_elements(
				CASE WHEN jsonb_typeof(o.detail_payload->'orderGoodsInfoList')='array'
				     THEN o.detail_payload->'orderGoodsInfoList' ELSE '[]'::jsonb END
			) item
			WHERE ($1::timestamptz IS NULL OR o.last_seen_at >= $1)
		), fallbacks AS (
			SELECT COALESCE(NULLIF(item->>'skuCode',''), NULLIF(item->>'sellerSku','')) AS platform_sku,
			       MAX(NULLIF(item->>'sellerSku','')) AS seller_sku
			FROM expanded
			GROUP BY COALESCE(NULLIF(item->>'skuCode',''), NULLIF(item->>'sellerSku',''))
		)
		SELECT e.order_no,
		       COALESCE(NULLIF(e.item->>'skuCode',''), NULLIF(e.item->>'sellerSku','')) AS platform_sku,
		       COALESCE(MAX(m.warehouse_sku), MAX(f.seller_sku), MAX(e.item->>'skuCode')),
		       MAX(COALESCE(e.item->>'goodsTitle','')),
		       MAX(COALESCE(e.item->>'sellerSku','')),
		       MAX(COALESCE(e.item->>'warehouseName','')),
		       COUNT(*)::numeric,
		       COALESCE(MAX(m.warehouse_qty),1),
		       CASE WHEN COUNT(m.shein_sku)>0 THEN 'shein_sku_mappings' ELSE 'seller_sku_fallback' END,
		       CASE WHEN COUNT(m.shein_sku)>0 THEN 'mapped' ELSE 'inferred' END,
		       MAX(NULLIF(e.item->>'sellerCurrencyPrice','')::numeric),
		       MAX(COALESCE(e.item->>'saleCurrency','')),
		       jsonb_build_object('items',jsonb_agg(e.item))
		FROM expanded e
		LEFT JOIN shein_beauty_hangers_home.shein_sku_mappings m
		  ON m.enabled AND m.shein_sku=e.item->>'skuCode'
		LEFT JOIN fallbacks f
		  ON f.platform_sku=COALESCE(NULLIF(e.item->>'skuCode',''), NULLIF(e.item->>'sellerSku',''))
		WHERE COALESCE(NULLIF(e.item->>'skuCode',''), NULLIF(e.item->>'sellerSku','')) IS NOT NULL
		GROUP BY e.order_no, COALESCE(NULLIF(e.item->>'skuCode',''), NULLIF(e.item->>'sellerSku',''))
		ORDER BY e.order_no, platform_sku
	`, nullableTime(since))
	if err != nil {
		return nil, nil, err
	}
	defer lineRows.Close()
	var lines []SourceLine
	for lineRows.Next() {
		var line SourceLine
		var raw []byte
		var unitPrice sql.NullFloat64
		var sourceWarehouse string
		line.Platform = "shein"
		line.ShopKey = "beauty-hangers-home"
		if err := lineRows.Scan(&line.OrderNo, &line.PlatformSKU, &line.SuggestedSKU,
			&line.ProductName, &line.VariantName, &sourceWarehouse, &line.Quantity,
			&line.ConversionFactor, &line.MappingSource, &line.MappingStatus,
			&unitPrice, &line.Currency, &raw); err != nil {
			return nil, nil, err
		}
		line.SourceLineKey = line.PlatformSKU
		line.WarehouseCode = matchWarehouseCode(sourceWarehouse, warehouses)
		if line.MappingStatus == "inferred" {
			if normalizedSKU, ok := matchWarehouseSKU(line.SuggestedSKU, inventory); ok {
				line.SuggestedSKU = normalizedSKU
				line.MappingSource = "xlwms_sku_prefix"
			}
		}
		if unitPrice.Valid {
			line.UnitPrice = &unitPrice.Float64
		}
		line.RawPayload = json.RawMessage(raw)
		lines = append(lines, line)
	}
	return orders, lines, lineRows.Err()
}

func (s *Syncer) readTemu(ctx context.Context, shopKey, schema string, inventory []InventoryRow, since time.Time) ([]SourceOrder, []SourceLine, error) {
	if schema != "temu_panda_homes" && schema != "temu_panda_buy" {
		return nil, nil, errors.New("unsupported Temu schema")
	}
	if err := s.temu.PingContext(ctx); err != nil {
		return nil, nil, err
	}
	orderQuery := fmt.Sprintf(`
		SELECT o.parent_order_sn, o.parent_order_status,
		       o.first_seen_at, o.first_seen_at, o.last_seen_at,
		       COALESCE(map.oms_warehouse_code, choice.selected_oms_warehouse_key, ''),
		       o.raw_payload
		FROM %s.temu_orders o
		LEFT JOIN LATERAL (
			SELECT selected_oms_warehouse_key
			FROM %s.temu_label_purchase_choices c
			WHERE c.parent_order_sn=o.parent_order_sn
			ORDER BY c.purchased_at DESC LIMIT 1
		) choice ON true
		LEFT JOIN public.temu_warehouse_mappings map
		  ON map.oms_warehouse_key=choice.selected_oms_warehouse_key
		WHERE ($1::timestamptz IS NULL OR o.last_seen_at >= $1)
		ORDER BY o.first_seen_at
	`, schema, schema)
	rows, err := s.temu.QueryContext(ctx, orderQuery, nullableTime(since))
	if err != nil {
		return nil, nil, err
	}
	var orders []SourceOrder
	for rows.Next() {
		var order SourceOrder
		var status int
		var raw []byte
		order.Platform = "temu"
		order.ShopKey = shopKey
		order.OccurredAtSource = "first_seen"
		order.SalesEligible = true
		if err := rows.Scan(&order.OrderNo, &status, &order.OccurredAt,
			&order.FirstSeenAt, &order.UpdatedAt, &order.WarehouseCode, &raw); err != nil {
			rows.Close()
			return nil, nil, err
		}
		order.SourceStatus = fmt.Sprintf("%d", status)
		order.NormalizedStatus = temuStatus(status)
		order.SalesEligible = status != 5 && status != 6
		order.RawPayload = json.RawMessage(raw)
		orders = append(orders, order)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}

	lineQuery := fmt.Sprintf(`
		SELECT l.parent_order_sn, l.order_sn, l.ext_code,
		       COALESCE(l.goods_name,''), COALESCE(l.spec,''), l.quantity,
		       COALESCE(map.oms_warehouse_code, choice.selected_oms_warehouse_key, ''),
		       l.raw_payload
		FROM %s.temu_order_lines l
		JOIN %s.temu_orders source_order ON source_order.parent_order_sn=l.parent_order_sn
		LEFT JOIN LATERAL (
			SELECT selected_oms_warehouse_key
			FROM %s.temu_label_purchase_choices c
			WHERE c.parent_order_sn=l.parent_order_sn
			ORDER BY c.purchased_at DESC LIMIT 1
		) choice ON true
		LEFT JOIN public.temu_warehouse_mappings map
		  ON map.oms_warehouse_key=choice.selected_oms_warehouse_key
		WHERE BTRIM(COALESCE(l.ext_code,'')) <> ''
		  AND ($1::timestamptz IS NULL OR source_order.last_seen_at >= $1)
		ORDER BY l.parent_order_sn, l.order_sn
	`, schema, schema, schema)
	lineRows, err := s.temu.QueryContext(ctx, lineQuery, nullableTime(since))
	if err != nil {
		return nil, nil, err
	}
	defer lineRows.Close()
	var lines []SourceLine
	for lineRows.Next() {
		var line SourceLine
		var raw []byte
		line.Platform = "temu"
		line.ShopKey = shopKey
		line.ConversionFactor = 1
		line.MappingSource = "platform_ext_code"
		line.MappingStatus = "identity"
		if err := lineRows.Scan(&line.OrderNo, &line.SourceLineKey, &line.PlatformSKU,
			&line.ProductName, &line.VariantName, &line.Quantity,
			&line.WarehouseCode, &raw); err != nil {
			return nil, nil, err
		}
		line.SuggestedSKU = line.PlatformSKU
		if normalizedSKU, ok := matchWarehouseSKU(line.SuggestedSKU, inventory); ok {
			line.SuggestedSKU = normalizedSKU
			if !strings.EqualFold(normalizedSKU, line.PlatformSKU) {
				line.MappingSource = "xlwms_sku_normalized"
				line.MappingStatus = "inferred"
			}
		}
		line.RawPayload = json.RawMessage(raw)
		var productRefs struct {
			ProductList []struct {
				ProductSKUID int64 `json:"productSkuId"`
			} `json:"productList"`
		}
		if json.Unmarshal(raw, &productRefs) == nil && len(productRefs.ProductList) > 0 {
			line.ProductSKUID = productRefs.ProductList[0].ProductSKUID
		}
		lines = append(lines, line)
	}
	return orders, lines, lineRows.Err()
}

func matchWarehouseCode(source string, warehouses []Warehouse) string {
	normalized := strings.ToUpper(strings.TrimSpace(source))
	if normalized == "" {
		return ""
	}
	codes := make([]string, 0, len(warehouses))
	for _, warehouse := range warehouses {
		codes = append(codes, strings.ToUpper(warehouse.Code))
	}
	sort.Slice(codes, func(i, j int) bool { return len(codes[i]) > len(codes[j]) })
	for _, code := range codes {
		if strings.Contains(normalized, code) {
			return code
		}
	}
	return ""
}

func matchWarehouseSKU(source string, inventory []InventoryRow) (string, bool) {
	normalizedSource := strings.ToUpper(strings.TrimSpace(source))
	if normalizedSource == "" {
		return "", false
	}
	candidates := make(map[string]string)
	for _, row := range inventory {
		normalizedSKU := strings.ToUpper(strings.TrimSpace(row.WarehouseSKU))
		if normalizedSKU != "" {
			candidates[normalizedSKU] = row.WarehouseSKU
		}
	}
	if exact, ok := candidates[normalizedSource]; ok {
		return exact, true
	}
	if missingUnit, ok := candidates[normalizedSource+"M"]; ok && strings.HasSuffix(normalizedSource, "C") {
		return missingUnit, true
	}
	best := ""
	for normalizedSKU, original := range candidates {
		if !strings.HasPrefix(normalizedSource, normalizedSKU+"-") {
			continue
		}
		suffix := strings.TrimPrefix(normalizedSource, normalizedSKU+"-")
		if suffix == "" || len(suffix) > 4 || !onlyDigits(suffix) {
			continue
		}
		if len(normalizedSKU) > len(strings.ToUpper(best)) {
			best = original
		}
	}
	return best, best != ""
}

func onlyDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}

func temuStatus(status int) string {
	switch status {
	case 2:
		return "pending_shipping"
	case 3:
		return "shipped"
	case 4:
		return "delivered"
	case 5:
		return "cancelled"
	default:
		return fmt.Sprintf("status_%d", status)
	}
}
