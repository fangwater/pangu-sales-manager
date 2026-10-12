package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Read the all-status ledger, falling back to stored full details for older history.
// Fulfillment queue timestamps and statuses must not override platform order truth.
func (s *Syncer) readTemu(ctx context.Context, shopKey, schema string, inventory []InventoryRow, since time.Time) ([]SourceOrder, []SourceLine, error) {
	if schema != "temu_panda_homes" && schema != "temu_panda_buy" {
		return nil, nil, errors.New("unsupported Temu schema")
	}
	query := fmt.Sprintf(`WITH source AS (
 SELECT parent_order_sn,first_seen_at,fetched_at AS updated_at,payload FROM %[1]s.temu_sales_orders
 UNION ALL
 SELECT o.parent_order_sn,o.first_seen_at,GREATEST(o.last_seen_at,d.fetched_at),
 COALESCE(CASE WHEN d.detail_payload->>'success'='true' THEN d.detail_payload->'result' END,o.raw_payload)
 FROM %[1]s.temu_orders o LEFT JOIN %[1]s.temu_order_details d USING(parent_order_sn)
 WHERE NOT EXISTS(SELECT 1 FROM %[1]s.temu_sales_orders a WHERE a.parent_order_sn=o.parent_order_sn))
 SELECT o.parent_order_sn,o.first_seen_at,o.updated_at,o.payload,
 COALESCE(map.oms_warehouse_code,choice.selected_oms_warehouse_key,'')
 FROM source o LEFT JOIN LATERAL (SELECT selected_oms_warehouse_key FROM %[1]s.temu_label_purchase_choices c WHERE c.parent_order_sn=o.parent_order_sn ORDER BY purchased_at DESC LIMIT 1) choice ON true
 LEFT JOIN public.temu_warehouse_mappings map ON map.oms_warehouse_key=choice.selected_oms_warehouse_key
 WHERE ($1::timestamptz IS NULL OR o.updated_at >= $1) ORDER BY o.parent_order_sn`, schema)
	rows, err := s.temu.QueryContext(ctx, query, nullableTime(since))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var orders []SourceOrder
	var lines []SourceLine
	for rows.Next() {
		order := SourceOrder{Platform: "temu", ShopKey: shopKey}
		var raw []byte
		if err = rows.Scan(&order.OrderNo, &order.FirstSeenAt, &order.UpdatedAt, &raw, &order.WarehouseCode); err != nil {
			return nil, nil, err
		}
		order.RawPayload = raw
		parsed, items, err := parseTemuSalesOrder(order, inventory)
		if err != nil {
			return nil, nil, fmt.Errorf("Temu order %s: %w", order.OrderNo, err)
		}
		orders = append(orders, parsed)
		lines = append(lines, items...)
	}
	return orders, lines, rows.Err()
}

func parseTemuSalesOrder(order SourceOrder, inventory []InventoryRow) (SourceOrder, []SourceLine, error) {
	var payload struct {
		Parent struct {
			Time   int64 `json:"parentOrderTime"`
			Status int   `json:"parentOrderStatus"`
		} `json:"parentOrderMap"`
		Lines []json.RawMessage `json:"orderList"`
	}
	if err := json.Unmarshal(order.RawPayload, &payload); err != nil {
		return order, nil, err
	}
	if payload.Parent.Status < 1 || payload.Lines == nil {
		return order, nil, errors.New("incomplete order snapshot")
	}
	order.SourceStatus = fmt.Sprint(payload.Parent.Status)
	order.NormalizedStatus = temuStatus(payload.Parent.Status)
	order.SalesEligible = payload.Parent.Status == 1 || payload.Parent.Status == 2 || payload.Parent.Status == 4 || payload.Parent.Status == 5
	order.OccurredAt, order.OccurredAtSource = order.FirstSeenAt, "first_seen"
	if payload.Parent.Time > 0 {
		order.OccurredAt = time.Unix(payload.Parent.Time, 0)
		order.OccurredAtSource = "platform_order_time"
	}
	var lines []SourceLine
	seen := map[string]bool{}
	for _, raw := range payload.Lines {
		var v struct {
			OrderSN      string  `json:"orderSn"`
			Status       int     `json:"orderStatus"`
			Quantity     float64 `json:"quantity"`
			ExtCode      string  `json:"extCode"`
			Name         string  `json:"goodsName"`
			OriginalName string  `json:"originalGoodsName"`
			Spec         string  `json:"spec"`
			OriginalSpec string  `json:"originalSpecName"`
			Products     []struct {
				SKU string `json:"extCode"`
				ID  int64  `json:"productSkuId"`
			} `json:"productList"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return order, nil, err
		}
		if v.OrderSN == "" || seen[v.OrderSN] || v.Quantity < 0 {
			return order, nil, errors.New("invalid order line")
		}
		seen[v.OrderSN] = true
		sku := strings.TrimSpace(v.ExtCode)
		var productID int64
		for _, p := range v.Products {
			if strings.TrimSpace(p.SKU) != "" {
				sku = strings.TrimSpace(p.SKU)
				productID = p.ID
				break
			}
		}
		// Unmapped lines remain visible to data-quality checks via the order's missing lines.
		if sku == "" {
			continue
		}
		qty := v.Quantity
		if v.Status == 3 || !order.SalesEligible {
			qty = 0
		}
		if v.OriginalName != "" {
			v.Name = v.OriginalName
		}
		if v.OriginalSpec != "" {
			v.Spec = v.OriginalSpec
		}
		line := SourceLine{Platform: "temu", ShopKey: order.ShopKey, OrderNo: order.OrderNo, SourceLineKey: v.OrderSN, PlatformSKU: sku, ProductSKUID: productID, SuggestedSKU: sku, ProductName: v.Name, VariantName: v.Spec, WarehouseCode: order.WarehouseCode, Quantity: qty, ConversionFactor: 1, MappingSource: "platform_ext_code", MappingStatus: "identity", RawPayload: raw}
		if normalized, ok := matchWarehouseSKU(sku, inventory); ok {
			line.SuggestedSKU = normalized
			if !strings.EqualFold(normalized, sku) {
				line.MappingSource = "xlwms_sku_normalized"
				line.MappingStatus = "inferred"
			}
		}
		lines = append(lines, line)
	}
	return order, lines, nil
}
