package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db *sql.DB
}

func openStore(ctx context.Context, databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect local database: %w", err)
	}
	store := &Store{db: db}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply database schema: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

type SyncPlan struct {
	Mode  string
	Since map[string]time.Time
}

func (s *Store) planSync(ctx context.Context) (SyncPlan, error) {
	plan := SyncPlan{Mode: "incremental", Since: make(map[string]time.Time)}
	var orderCount int64
	var lastFull sql.NullTime
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM normalized_orders`).Scan(&orderCount); err != nil {
		return plan, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT MAX(completed_at) FROM sync_runs
		WHERE status='succeeded' AND sync_mode='full'
	`).Scan(&lastFull); err != nil {
		return plan, err
	}
	if orderCount == 0 || !lastFull.Valid || time.Since(lastFull.Time) >= 24*time.Hour {
		plan.Mode = "full"
		return plan, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT platform,shop_key,MAX(source_updated_at)
		FROM normalized_orders
		GROUP BY platform,shop_key
	`)
	if err != nil {
		return plan, err
	}
	defer rows.Close()
	for rows.Next() {
		var platform, shop string
		var watermark sql.NullTime
		if err := rows.Scan(&platform, &shop, &watermark); err != nil {
			return plan, err
		}
		if watermark.Valid {
			plan.Since[platform+"\x00"+shop] = watermark.Time.Add(-5 * time.Minute)
		}
	}
	return plan, rows.Err()
}

func (s *Store) beginSync(ctx context.Context, mode string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO sync_runs(status,sync_mode) VALUES ('running',$1) RETURNING id
	`, mode).Scan(&id)
	return id, err
}

func (s *Store) finishSync(ctx context.Context, id int64, status string, counts SyncCounts, syncErr error) error {
	errorMessage := ""
	if syncErr != nil {
		errorMessage = syncErr.Error()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE sync_runs
		SET status=$2, completed_at=now(), orders_synced=$3, lines_synced=$4,
		    inventory_synced=$5, error_message=$6
		WHERE id=$1
	`, id, status, counts.Orders, counts.Lines, counts.Inventory, errorMessage)
	return err
}

func (s *Store) upsertSourceBatch(ctx context.Context, orders []SourceOrder, lines []SourceLine) (int, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	orderIDs := make(map[string]int64, len(orders))
	for _, order := range orders {
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO normalized_orders(
				platform, shop_key, source_order_no, source_status, normalized_status,
				occurred_at, occurred_at_source, warehouse_code, sales_eligible,
				source_first_seen_at, source_updated_at, raw_payload, synced_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now())
			ON CONFLICT (platform, shop_key, source_order_no) DO UPDATE SET
				source_status=EXCLUDED.source_status,
				normalized_status=EXCLUDED.normalized_status,
				occurred_at=EXCLUDED.occurred_at,
				occurred_at_source=EXCLUDED.occurred_at_source,
				warehouse_code=CASE WHEN EXCLUDED.warehouse_code <> '' THEN EXCLUDED.warehouse_code ELSE normalized_orders.warehouse_code END,
				sales_eligible=EXCLUDED.sales_eligible,
				source_first_seen_at=EXCLUDED.source_first_seen_at,
				source_updated_at=EXCLUDED.source_updated_at,
				raw_payload=EXCLUDED.raw_payload,
				synced_at=now()
			RETURNING id
		`, order.Platform, order.ShopKey, order.OrderNo, order.SourceStatus,
			order.NormalizedStatus, order.OccurredAt, order.OccurredAtSource,
			order.WarehouseCode, order.SalesEligible, nullableTime(order.FirstSeenAt),
			nullableTime(order.UpdatedAt), validJSON(order.RawPayload)).Scan(&id)
		if err != nil {
			return 0, 0, fmt.Errorf("upsert %s order %s: %w", order.Platform, order.OrderNo, err)
		}
		orderIDs[orderKey(order.Platform, order.ShopKey, order.OrderNo)] = id
	}

	lineCount := 0
	for _, line := range lines {
		orderID, ok := orderIDs[orderKey(line.Platform, line.ShopKey, line.OrderNo)]
		if !ok {
			continue
		}
		warehouseSKU, conversion, err := upsertMapping(ctx, tx, line)
		if err != nil {
			return 0, 0, fmt.Errorf("map %s SKU %s: %w", line.Platform, line.PlatformSKU, err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO normalized_order_lines(
				order_id, source_line_key, platform_sku, warehouse_sku, product_name,
				variant_name, warehouse_code, quantity, conversion_factor,
				warehouse_quantity, unit_price, currency, raw_payload, synced_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$8::numeric*$9::numeric,$10,$11,$12,now())
			ON CONFLICT (order_id, source_line_key) DO UPDATE SET
				platform_sku=EXCLUDED.platform_sku,
				warehouse_sku=EXCLUDED.warehouse_sku,
				product_name=EXCLUDED.product_name,
				variant_name=EXCLUDED.variant_name,
				warehouse_code=CASE WHEN EXCLUDED.warehouse_code <> '' THEN EXCLUDED.warehouse_code ELSE normalized_order_lines.warehouse_code END,
				quantity=EXCLUDED.quantity,
				conversion_factor=EXCLUDED.conversion_factor,
				warehouse_quantity=EXCLUDED.warehouse_quantity,
				unit_price=EXCLUDED.unit_price,
				currency=EXCLUDED.currency,
				raw_payload=EXCLUDED.raw_payload,
				synced_at=now()
		`, orderID, line.SourceLineKey, line.PlatformSKU, warehouseSKU,
			line.ProductName, line.VariantName, line.WarehouseCode, line.Quantity,
			conversion, line.UnitPrice, line.Currency, validJSON(line.RawPayload))
		if err != nil {
			return 0, 0, fmt.Errorf("upsert %s line %s: %w", line.Platform, line.SourceLineKey, err)
		}
		lineCount++
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(orderIDs), lineCount, nil
}

func upsertMapping(ctx context.Context, tx *sql.Tx, line SourceLine) (string, float64, error) {
	warehouseSKU := strings.TrimSpace(line.SuggestedSKU)
	if warehouseSKU == "" {
		warehouseSKU = strings.TrimSpace(line.PlatformSKU)
	}
	if warehouseSKU == "" {
		return "", 0, errors.New("warehouse SKU is empty")
	}
	factor := line.ConversionFactor
	if factor <= 0 {
		factor = 1
	}
	status := line.MappingStatus
	if status == "" {
		status = "inferred"
	}
	source := line.MappingSource
	if source == "" {
		source = "source"
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO canonical_skus(warehouse_sku, product_name)
		VALUES ($1,$2)
		ON CONFLICT (warehouse_sku) DO UPDATE SET
			product_name=CASE WHEN canonical_skus.product_name='' THEN EXCLUDED.product_name ELSE canonical_skus.product_name END,
			updated_at=now()
	`, warehouseSKU, line.ProductName); err != nil {
		return "", 0, err
	}

	err := tx.QueryRowContext(ctx, `
		INSERT INTO sku_mappings(
			platform, shop_key, platform_sku, warehouse_sku, conversion_factor,
			mapping_source, mapping_status, product_name, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
		ON CONFLICT (platform, shop_key, platform_sku) DO UPDATE SET
			warehouse_sku=CASE WHEN sku_mappings.mapping_status='manual' THEN sku_mappings.warehouse_sku ELSE EXCLUDED.warehouse_sku END,
			conversion_factor=CASE WHEN sku_mappings.mapping_status='manual' THEN sku_mappings.conversion_factor ELSE EXCLUDED.conversion_factor END,
			mapping_source=CASE WHEN sku_mappings.mapping_status='manual' THEN sku_mappings.mapping_source ELSE EXCLUDED.mapping_source END,
			mapping_status=CASE WHEN sku_mappings.mapping_status='manual' THEN sku_mappings.mapping_status ELSE EXCLUDED.mapping_status END,
			product_name=CASE WHEN EXCLUDED.product_name<>'' THEN EXCLUDED.product_name ELSE sku_mappings.product_name END,
			updated_at=now()
		RETURNING warehouse_sku, conversion_factor
	`, line.Platform, line.ShopKey, line.PlatformSKU, warehouseSKU, factor,
		source, status, line.ProductName).Scan(&warehouseSKU, &factor)
	return warehouseSKU, factor, err
}

func (s *Store) replaceInventory(ctx context.Context, warehouses []Warehouse, inventory []InventoryRow) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, warehouse := range warehouses {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO warehouses(code,name,active,source,updated_at)
			VALUES ($1,$2,$3,'xlwms',now())
			ON CONFLICT (code) DO UPDATE SET name=EXCLUDED.name,active=EXCLUDED.active,updated_at=now()
		`, warehouse.Code, warehouse.Name, warehouse.Active)
		if err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM warehouse_inventory`); err != nil {
		return 0, err
	}
	for _, row := range inventory {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canonical_skus(warehouse_sku,product_name)
			VALUES ($1,'') ON CONFLICT (warehouse_sku) DO NOTHING
		`, row.WarehouseSKU); err != nil {
			return 0, err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO warehouse_inventory(
				warehouse_code,warehouse_sku,warehouse_name,total_quantity,
				available_quantity,locked_quantity,in_transit_quantity,
				statistic_date,source_updated_at,synced_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
		`, row.WarehouseCode, row.WarehouseSKU, row.WarehouseName, row.Total,
			row.Available, row.Locked, row.InTransit, row.StatisticDate, row.UpdatedAt)
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(inventory), nil
}

func (s *Store) updateManualMapping(ctx context.Context, platform, shop, platformSKU, warehouseSKU string, factor float64) error {
	if factor <= 0 {
		return errors.New("conversion factor must be greater than zero")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO canonical_skus(warehouse_sku) VALUES ($1)
		ON CONFLICT (warehouse_sku) DO NOTHING
	`, warehouseSKU); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE sku_mappings SET warehouse_sku=$4,conversion_factor=$5,
			mapping_source='operator',mapping_status='manual',updated_at=now()
		WHERE platform=$1 AND shop_key=$2 AND platform_sku=$3
	`, platform, shop, platformSKU, warehouseSKU, factor)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return sql.ErrNoRows
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE normalized_order_lines l SET
			warehouse_sku=$4,conversion_factor=$5,warehouse_quantity=l.quantity*$5,synced_at=now()
		FROM normalized_orders o
		WHERE l.order_id=o.id AND o.platform=$1 AND o.shop_key=$2 AND l.platform_sku=$3
	`, platform, shop, platformSKU, warehouseSKU, factor)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func orderKey(platform, shop, orderNo string) string {
	return platform + "\x00" + shop + "\x00" + orderNo
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func validJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || !json.Valid(value) {
		return json.RawMessage(`{}`)
	}
	return value
}
