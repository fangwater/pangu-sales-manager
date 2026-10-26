package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Cost valuation must replay shared warehouse demand from every shop/platform.
// This endpoint exposes accounting references and quantities only, never buyers.
func (s *APIServer) fifoDemands(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if strings.TrimSpace(r.URL.Query().Get("start")) == "" {
		writeJSON(w, 400, apiResponse{Success: false, Error: "须填写成本核算起始日"})
		return
	}
	start, end, err := profitReportRange(r, s.timezone)
	if err != nil || end.Sub(start) > 1462*24*time.Hour {
		writeJSON(w, 400, apiResponse{Success: false, Error: "核算日期无效或超过四年"})
		return
	}
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		s.internalError(w, "begin FIFO demand snapshot", err)
		return
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT l.id,o.id,o.platform,o.shop_key,o.source_order_no,l.source_line_key,
 o.occurred_at,(o.occurred_at AT TIME ZONE $3)::date::text,l.platform_sku,l.warehouse_sku,
 COALESCE(NULLIF(l.warehouse_code,''),o.warehouse_code,''),l.quantity::text,l.warehouse_quantity::text,l.conversion_factor::text
 FROM normalized_orders o JOIN normalized_order_lines l ON l.order_id=o.id
 WHERE o.sales_eligible AND o.occurred_at >= $1 AND o.occurred_at < $2 AND l.warehouse_quantity>0
 ORDER BY o.occurred_at,o.id,l.id LIMIT 200001`, start, end, s.timezone)
	if err != nil {
		s.internalError(w, "load FIFO demands", err)
		return
	}
	type demand struct {
		ID                int64     `json:"id"`
		OrderID           int64     `json:"order_id"`
		Platform          string    `json:"platform"`
		Shop              string    `json:"shop_key"`
		Order             string    `json:"order_no"`
		Line              string    `json:"line_no"`
		At                time.Time `json:"occurred_at"`
		Date              string    `json:"date"`
		SKU               string    `json:"platform_sku"`
		WarehouseSKU      string    `json:"warehouse_sku"`
		Warehouse         string    `json:"warehouse_code"`
		Quantity          string    `json:"quantity"`
		WarehouseQuantity string    `json:"warehouse_quantity"`
		Factor            string    `json:"conversion_factor"`
	}
	result := []demand{}
	for rows.Next() {
		var row demand
		if err := rows.Scan(&row.ID, &row.OrderID, &row.Platform, &row.Shop, &row.Order, &row.Line, &row.At, &row.Date, &row.SKU, &row.WarehouseSKU, &row.Warehouse, &row.Quantity, &row.WarehouseQuantity, &row.Factor); err != nil {
			rows.Close()
			s.internalError(w, "scan FIFO demands", err)
			return
		}
		result = append(result, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		s.internalError(w, "read FIFO demands", err)
		return
	}
	if len(result) > 200000 {
		writeJSON(w, 413, apiResponse{Success: false, Error: "订单明细超过 200000 行，无法提供完整 FIFO 快照"})
		return
	}
	var missing, unknownTime int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE counts.lines=0 OR o.source_item_count>counts.lines),COUNT(*) FILTER(WHERE o.occurred_at_source='first_seen')
 FROM normalized_orders o CROSS JOIN LATERAL(SELECT COUNT(*) lines FROM normalized_order_lines l WHERE l.order_id=o.id) counts
 WHERE o.sales_eligible AND o.occurred_at >= $1 AND o.occurred_at < $2`, start, end).Scan(&missing, &unknownTime)
	if err != nil {
		s.internalError(w, "check FIFO demand coverage", err)
		return
	}
	var sourceAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT MAX(completed_at) FROM sync_runs WHERE status='succeeded'`).Scan(&sourceAt); err != nil {
		s.internalError(w, "load FIFO source time", err)
		return
	}
	payload, err := json.Marshal(map[string]any{"version": 1, "timezone": s.timezone, "start": start, "end": end, "missing_line_orders": missing, "unknown_order_time_orders": unknownTime, "demands": result})
	if err != nil {
		s.internalError(w, "encode FIFO demands", err)
		return
	}
	digest := sha256.Sum256(payload)
	writeJSON(w, 200, apiResponse{Success: true, Data: map[string]any{"version": 1, "timezone": s.timezone, "start": start.Format("2006-01-02"), "end": end.AddDate(0, 0, -1).Format("2006-01-02"), "generated_at": time.Now(), "source_updated_at": nullTimePointer(sourceAt), "sha256": hex.EncodeToString(digest[:]), "missing_line_orders": missing, "unknown_order_time_orders": unknownTime, "demands": result}})
}
func nullTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
