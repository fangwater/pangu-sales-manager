package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type MappingRecord struct {
	Platform         string    `json:"platform"`
	ShopKey          string    `json:"shop_key"`
	PlatformSKU      string    `json:"platform_sku"`
	WarehouseSKU     string    `json:"warehouse_sku"`
	ConversionFactor float64   `json:"conversion_factor"`
	MappingSource    string    `json:"mapping_source"`
	MappingStatus    string    `json:"mapping_status"`
	ProductName      string    `json:"product_name"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type MappingPage struct {
	Items []MappingRecord `json:"items"`
	Total int             `json:"total"`
	Page  int             `json:"page"`
	Size  int             `json:"page_size"`
	SKUs  []string        `json:"warehouse_sku_options"`
}

type OrderListItem struct {
	Platform         string          `json:"platform"`
	ShopKey          string          `json:"shop_key"`
	OrderNo          string          `json:"order_no"`
	Status           string          `json:"status"`
	NormalizedStatus string          `json:"normalized_status"`
	OccurredAt       time.Time       `json:"occurred_at"`
	OccurredAtSource string          `json:"occurred_at_source"`
	WarehouseCode    string          `json:"warehouse_code"`
	SalesEligible    bool            `json:"sales_eligible"`
	Lines            json.RawMessage `json:"lines"`
}

type OrderPage struct {
	Items []OrderListItem `json:"items"`
	Total int             `json:"total"`
	Page  int             `json:"page"`
	Size  int             `json:"page_size"`
}

func (s *Store) listMappings(ctx context.Context, platform, shop, status, query string, page, size int) (MappingPage, error) {
	result := MappingPage{Page: page, Size: size, Items: []MappingRecord{}, SKUs: []string{}}
	likeQuery := "%" + strings.TrimSpace(query) + "%"
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sku_mappings
		WHERE ($1='' OR platform=$1) AND ($2='' OR shop_key=$2)
		  AND ($3='' OR mapping_status=$3)
		  AND ($4='%%' OR platform_sku ILIKE $4 OR warehouse_sku ILIKE $4 OR product_name ILIKE $4)
	`, platform, shop, status, likeQuery).Scan(&result.Total)
	if err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT platform,shop_key,platform_sku,warehouse_sku,conversion_factor,
		       mapping_source,mapping_status,product_name,updated_at
		FROM sku_mappings
		WHERE ($1='' OR platform=$1) AND ($2='' OR shop_key=$2)
		  AND ($3='' OR mapping_status=$3)
		  AND ($4='%%' OR platform_sku ILIKE $4 OR warehouse_sku ILIKE $4 OR product_name ILIKE $4)
		ORDER BY CASE mapping_status WHEN 'unmapped' THEN 0 WHEN 'inferred' THEN 1 ELSE 2 END,
		         platform,shop_key,platform_sku
		LIMIT $5 OFFSET $6
	`, platform, shop, status, likeQuery, size, (page-1)*size)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item MappingRecord
		if err := rows.Scan(&item.Platform, &item.ShopKey, &item.PlatformSKU, &item.WarehouseSKU,
			&item.ConversionFactor, &item.MappingSource, &item.MappingStatus,
			&item.ProductName, &item.UpdatedAt); err != nil {
			rows.Close()
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	optionRows, err := s.db.QueryContext(ctx, `
		SELECT warehouse_sku FROM canonical_skus ORDER BY warehouse_sku
	`)
	if err != nil {
		return result, err
	}
	defer optionRows.Close()
	for optionRows.Next() {
		var sku string
		if err := optionRows.Scan(&sku); err != nil {
			return result, err
		}
		result.SKUs = append(result.SKUs, sku)
	}
	return result, optionRows.Err()
}

func (s *Store) listOrders(ctx context.Context, platform, shop, sku string, page, size int) (OrderPage, error) {
	result := OrderPage{Page: page, Size: size, Items: []OrderListItem{}}
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM normalized_orders o
		WHERE ($1='' OR o.platform=$1) AND ($2='' OR o.shop_key=$2)
		  AND ($3='' OR EXISTS (
			SELECT 1 FROM normalized_order_lines line
			WHERE line.order_id=o.id AND line.warehouse_sku=$3
		  ))
	`, platform, shop, sku).Scan(&result.Total)
	if err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.platform,o.shop_key,o.source_order_no,o.source_status,o.normalized_status,
		       o.occurred_at,o.occurred_at_source,o.warehouse_code,o.sales_eligible,
		       COALESCE(jsonb_agg(jsonb_build_object(
			   'platform_sku',l.platform_sku,
			   'warehouse_sku',l.warehouse_sku,
			   'product_name',l.product_name,
			   'quantity',l.quantity,
			   'conversion_factor',l.conversion_factor,
			   'warehouse_quantity',l.warehouse_quantity,
			   'warehouse_code',l.warehouse_code
		   ) ORDER BY l.id) FILTER (WHERE l.id IS NOT NULL),'[]'::jsonb)
		FROM normalized_orders o
		LEFT JOIN normalized_order_lines l ON l.order_id=o.id
		WHERE ($1='' OR o.platform=$1) AND ($2='' OR o.shop_key=$2)
		  AND ($3='' OR EXISTS (
			SELECT 1 FROM normalized_order_lines line
			WHERE line.order_id=o.id AND line.warehouse_sku=$3
		  ))
		GROUP BY o.id
		ORDER BY o.occurred_at DESC
		LIMIT $4 OFFSET $5
	`, platform, shop, sku, size, (page-1)*size)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item OrderListItem
		var lines []byte
		if err := rows.Scan(&item.Platform, &item.ShopKey, &item.OrderNo, &item.Status,
			&item.NormalizedStatus, &item.OccurredAt, &item.OccurredAtSource,
			&item.WarehouseCode, &item.SalesEligible, &lines); err != nil {
			return result, err
		}
		item.Lines = json.RawMessage(lines)
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
}

func (s *Store) listWarehouses(ctx context.Context) ([]WarehouseSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.code,w.name,COALESCE(SUM(i.available_quantity),0),COUNT(i.warehouse_sku)
		FROM warehouses w
		LEFT JOIN warehouse_inventory i ON i.warehouse_code=w.code
		WHERE w.active
		GROUP BY w.code,w.name ORDER BY w.code
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []WarehouseSummary{}
	for rows.Next() {
		var item WarehouseSummary
		if err := rows.Scan(&item.Code, &item.Name, &item.AvailableStock, &item.ActiveSKUCount); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func normalizedPage(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

func scanNullableString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func dataError(operation string, err error) error {
	return fmt.Errorf("%s: %w", operation, err)
}
