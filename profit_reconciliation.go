package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Store) queryProfitPendingChecks(ctx context.Context, shopKey string) ([]ProfitPendingCheck, error) {
	rows, err := s.db.QueryContext(ctx, `
	 WITH orders AS (
	  SELECT shop_key,currency,
	   COUNT(*) FILTER(WHERE status IN ('pending','partial','review')) AS orders,
	   COALESCE(SUM(remaining_amount) FILTER(WHERE status IN ('pending','partial','review')),0) AS net_amount,
	   COUNT(*) FILTER(WHERE status IN ('pending','partial','review') AND (sales_receipt_after_discount<>0 OR freight_receipt_after_discount<>0)) AS discounted_rows,
	   COUNT(*) FILTER(WHERE snapshot_active AND posted_rows>0) AS overlap_orders,
	   COALESCE(SUM(raw_amount) FILTER(WHERE snapshot_active AND posted_rows>0),0) AS overlap_net_amount,
	   COUNT(*) AS raw_orders,SUM(raw_amount) AS raw_net_amount,
	   COUNT(*) FILTER(WHERE status='settled') AS settled_orders,
	   COUNT(*) FILTER(WHERE status='partial') AS partial_orders,
	   COUNT(*) FILTER(WHERE status='review') AS review_orders,
	   COUNT(*) FILTER(WHERE status='archived') AS archived_orders
	  FROM profit_pending_order_reconciliation WHERE ($1='' OR shop_key=$1) GROUP BY 1,2
	 ), labels AS (
	  SELECT shop_key,currency,
	   COALESCE(SUM(source_rows) FILTER(WHERE status IN ('pending','partial','review')),0) AS label_rows,
	   COALESCE(SUM(remaining_amount) FILTER(WHERE status IN ('pending','partial','review')),0) AS label_amount,
	   COALESCE(SUM(source_rows) FILTER(WHERE snapshot_active AND posted_rows>0),0) AS posted_overlap_rows,
	   COALESCE(SUM(matched_amount) FILTER(WHERE snapshot_active),0) AS posted_overlap_amount,
	   SUM(source_rows) AS raw_label_rows,SUM(raw_amount) AS raw_label_amount,
	   COALESCE(SUM(source_rows) FILTER(WHERE status='settled'),0) AS settled_label_rows,
	   COALESCE(SUM(source_rows) FILTER(WHERE status='partial'),0) AS partial_label_rows,
	   COALESCE(SUM(source_rows) FILTER(WHERE status='review'),0) AS review_label_rows,
	   COALESCE(SUM(source_rows) FILTER(WHERE status='archived'),0) AS archived_label_rows
	  FROM profit_pending_shipping_reconciliation WHERE ($1='' OR shop_key=$1) GROUP BY 1,2
	 )
	 SELECT COALESCE(o.shop_key,l.shop_key),COALESCE(o.currency,l.currency),
	  COALESCE(orders,0),COALESCE(net_amount,0),COALESCE(discounted_rows,0),COALESCE(overlap_orders,0),COALESCE(overlap_net_amount,0),
	  COALESCE(label_rows,0),COALESCE(label_amount,0),COALESCE(posted_overlap_rows,0),COALESCE(posted_overlap_amount,0),
	  COALESCE(raw_orders,0),COALESCE(raw_net_amount,0),COALESCE(settled_orders,0),COALESCE(partial_orders,0),COALESCE(review_orders,0),COALESCE(archived_orders,0),
	  COALESCE(raw_label_rows,0),COALESCE(raw_label_amount,0),COALESCE(settled_label_rows,0),COALESCE(partial_label_rows,0),COALESCE(review_label_rows,0),COALESCE(archived_label_rows,0)
	 FROM orders o FULL JOIN labels l USING(shop_key,currency) ORDER BY 1,2`, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProfitPendingCheck{}
	for rows.Next() {
		var check ProfitPendingCheck
		if err := rows.Scan(&check.ShopKey, &check.Currency, &check.Orders, &check.NetAmount, &check.DiscountedRows, &check.OverlapOrders,
			&check.OverlapNetAmount, &check.LabelRows, &check.LabelAmount, &check.PostedOverlapRows, &check.PostedOverlapAmount,
			&check.RawOrders, &check.RawNetAmount, &check.SettledOrders, &check.PartialOrders, &check.ReviewOrders, &check.ArchivedOrders,
			&check.RawLabelRows, &check.RawLabelAmount, &check.SettledLabelRows, &check.PartialLabelRows, &check.ReviewLabelRows, &check.ArchivedLabelRows); err != nil {
			return nil, err
		}
		result = append(result, check)
	}
	return result, rows.Err()
}

type ProfitReconciliationResponse struct {
	Kind     string            `json:"kind"`
	Rows     []json.RawMessage `json:"rows"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

func (s *APIServer) profitReconciliation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	view := map[string]string{"orders": "profit_pending_order_reconciliation", "shipping": "profit_pending_shipping_reconciliation"}[kind]
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = "open"
	}
	validStatus := map[string]bool{"all": true, "open": true, "pending": true, "partial": true, "settled": true, "review": true, "archived": true}
	if view == "" || !validStatus[status] {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Error: "核销类型或状态无效"})
		return
	}
	page := normalizedPage(queryInt(r, "page", 1), 1, 100000)
	pageSize := normalizedPage(queryInt(r, "page_size", 30), 30, 200)
	keyword := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(keyword)) > 200 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Error: "查询条件不能超过 200 字"})
		return
	}
	result := ProfitReconciliationResponse{Kind: kind, Page: page, PageSize: pageSize, Rows: []json.RawMessage{}}
	var payload []byte
	err := s.store.db.QueryRowContext(ctx, fmt.Sprintf(`
	 WITH filtered AS MATERIALIZED (
	  SELECT * FROM %s WHERE ($1='' OR shop_key=$1)
	   AND ($2='all' OR ($2='open' AND status IN ('pending','partial','review')) OR status=$2)
	   AND ($3='' OR reference ILIKE $3 OR secondary_reference ILIKE $3)
	 ), paged AS (SELECT * FROM filtered ORDER BY imported_at DESC,id DESC LIMIT $4 OFFSET $5)
	 SELECT (SELECT COUNT(*) FROM filtered),COALESCE((SELECT jsonb_agg(to_jsonb(paged) ORDER BY imported_at DESC,id DESC) FROM paged),'[]'::jsonb)
	`, view), strings.TrimSpace(r.URL.Query().Get("shop_key")), status, reconciliationKeyword(keyword), pageSize, (page-1)*pageSize).Scan(&result.Total, &payload)
	if err != nil {
		s.internalError(w, "load profit reconciliation", err)
		return
	}
	if err := json.Unmarshal(payload, &result.Rows); err != nil {
		s.internalError(w, "decode profit reconciliation", err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: result})
}

func reconciliationKeyword(keyword string) string {
	if keyword == "" {
		return ""
	}
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword) + "%"
}
