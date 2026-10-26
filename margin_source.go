package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

// Cash evidence for an order cohort, retaining unassigned source transactions.
// Numeric values cross the service boundary as decimal strings.
func (s *APIServer) marginSource(w http.ResponseWriter, r *http.Request) {
	start, end, err := profitReportRange(r, s.timezone)
	if err != nil || end.Sub(start) > 1462*24*time.Hour {
		writeJSON(w, 400, apiResponse{Success: false, Error: "核算日期无效或超过四年"})
		return
	}
	asof := end
	if raw := r.URL.Query().Get("as_of"); raw != "" {
		location, _ := time.LoadLocation(s.timezone)
		d, e := time.ParseInLocation("2006-01-02", raw, location)
		if e != nil || d.Before(end.AddDate(0, 0, -1)) {
			writeJSON(w, 400, apiResponse{Success: false, Error: "到账截止日不得早于订单截止日"})
			return
		}
		asof = d.AddDate(0, 0, 1)
	}
	if asof.Sub(start) > 1462*24*time.Hour {
		writeJSON(w, 400, apiResponse{Success: false, Error: "到账截止日与订单开始日相差不得超过四年"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		s.internalError(w, "begin margin source", err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL jit=off; SET LOCAL enable_nestloop=off`); err != nil {
		s.internalError(w, "prepare margin source", err)
		return
	}
	var items, ledger, records, shops, allocation []byte
	err = tx.QueryRowContext(ctx, `WITH cohort AS MATERIALIZED(
 SELECT o.id,o.shop_key,o.source_order_no,o.occurred_at,o.normalized_status,o.sales_eligible,o.source_item_count,o.occurred_at_source,
 COALESCE(o.raw_payload->'parentOrderMap'->>'siteName',o.raw_payload->'parentOrderMap'->>'siteId',o.raw_payload->>'siteName',o.raw_payload->>'siteId','') site
 FROM normalized_orders o WHERE o.platform='temu' AND o.occurred_at >= $1 AND o.occurred_at < $2 AND ($4='' OR o.shop_key=$4)),
 items AS(SELECT jsonb_build_object('order_id',o.id,'line_id',l.id,'shop_key',o.shop_key,'order_no',o.source_order_no,'order_date',(o.occurred_at AT TIME ZONE $5)::date::text,
 'order_time_source',o.occurred_at_source,'status',o.normalized_status,'sales_eligible',o.sales_eligible,'platform_sku',COALESCE(l.platform_sku,''),'warehouse_sku',COALESCE(l.warehouse_sku,''),
 'warehouse_code',COALESCE(NULLIF(l.warehouse_code,''),''),'quantity',l.quantity::text,'warehouse_quantity',l.warehouse_quantity::text,'conversion_factor',l.conversion_factor::text,
 'product_name',l.product_name,'variant_name',l.variant_name,'sku_id',l.platform_sku_id::text,'product_sku_id',l.platform_product_sku_id::text,
 'product_id',COALESCE(l.raw_payload->>'goodsId',l.raw_payload->>'productId',''),'site',o.site,
 'source_item_count',o.source_item_count,'normalized_line_count',(SELECT count(*) FROM normalized_order_lines x WHERE x.order_id=o.id)) item FROM cohort o LEFT JOIN normalized_order_lines l ON l.order_id=o.id),
 cash AS MATERIALIZED(SELECT * FROM profit_ledger_for_range('1900-01-01'::timestamptz,$3,$4,'','',true)),
 cohort_cash_keys AS MATERIALIZED(
 SELECT c.entry_key FROM cash c JOIN cohort o ON o.shop_key=c.shop_key AND o.source_order_no=c.po_no
 UNION SELECT c.entry_key FROM cash c CROSS JOIN LATERAL unnest(c.candidate_pos) p(po_no) JOIN cohort o ON o.shop_key=c.shop_key AND o.source_order_no=p.po_no
 UNION SELECT c.entry_key FROM cash c WHERE c.allocation_status='unattributed' AND (c.occurred_at >= $1 OR c.occurred_at IS NULL)),
 selected_cash AS(SELECT c.* FROM cash c JOIN cohort_cash_keys k USING(entry_key)),
 evidence AS(SELECT jsonb_build_object('entry_key',c.entry_key,'shop_key',c.shop_key,'source',c.source,'source_reference',c.source_reference,'fee_name',c.fee_name,'order_no',c.po_no,'platform_sku',c.platform_sku,
 'currency',c.currency,'amount',c.amount::text,'original_amount',c.original_amount::text,'date',(c.occurred_at AT TIME ZONE $5)::date::text,'occurred_at',c.occurred_at,'paid_at',c.paid_at,
 'package_no',c.package_no,'tracking_no',c.tracking_no,'order_status',c.order_status,'sku_status',c.sku_status,'allocation_status',c.allocation_status,'allocation_reason',c.allocation_reason,
 'allocations',COALESCE((SELECT jsonb_agg(a||jsonb_build_object('amount',a->>'amount','weight',a->>'weight','total_weight',a->>'total_weight')) FROM jsonb_array_elements(c.allocations) a),'[]'::jsonb)) item FROM selected_cash c)
 SELECT COALESCE((SELECT jsonb_agg(item ORDER BY item->>'shop_key',item->>'order_no',item->>'line_id') FROM items),'[]'::jsonb),COALESCE((SELECT jsonb_agg(item ORDER BY item->>'entry_key') FROM evidence),'[]'::jsonb),
 COALESCE((SELECT jsonb_agg(payload||jsonb_build_object('id',id,'version',version) ORDER BY id) FROM profit_accounting_records WHERE deleted_at IS NULL AND ($4='' OR payload->>'shop_key'=$4 OR kind='metadata')),'[]'::jsonb),
 COALESCE((SELECT jsonb_agg(shop_key ORDER BY shop_key) FROM(SELECT DISTINCT shop_key FROM normalized_orders WHERE platform='temu') s),'[]'::jsonb),
 jsonb_build_object('policy',(SELECT to_jsonb(p) FROM profit_allocation_policy p WHERE id=1),'basis',COALESCE((SELECT jsonb_agg(to_jsonb(b) ORDER BY b.shop_key,b.po_no) FROM profit_allocation_basis b WHERE EXISTS(SELECT 1 FROM cohort o WHERE o.shop_key=b.shop_key AND o.source_order_no=b.po_no)),'[]'::jsonb))`, start, end, asof, r.URL.Query().Get("shop_key"), s.timezone).Scan(&items, &ledger, &records, &shops, &allocation)
	if err != nil {
		s.internalError(w, "load margin source", err)
		return
	}
	var lineCount int
	var lines []json.RawMessage
	if err = json.Unmarshal(items, &lines); err != nil {
		s.internalError(w, "decode margin source", err)
		return
	}
	lineCount = len(lines)
	if lineCount > 200000 {
		writeJSON(w, 413, apiResponse{Success: false, Error: "订单超过 200000 行，不能提供完整核算快照"})
		return
	}
	payload := map[string]any{"version": 1, "timezone": s.timezone, "start": start.Format("2006-01-02"), "end": end.AddDate(0, 0, -1).Format("2006-01-02"), "as_of": asof.AddDate(0, 0, -1).Format("2006-01-02"), "items": json.RawMessage(items), "cash": json.RawMessage(ledger), "records": json.RawMessage(records), "shops": json.RawMessage(shops), "allocation": json.RawMessage(allocation)}
	raw, _ := json.Marshal(payload)
	digest := sha256.Sum256(raw)
	payload["sha256"] = hex.EncodeToString(digest[:])
	writeJSON(w, 200, apiResponse{Success: true, Data: payload})
}
