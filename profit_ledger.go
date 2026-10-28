package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type profitLedgerFilter struct {
	start, end                              time.Time
	shop, source, status, currency, keyword string
	undated                                 bool
}

func parseProfitLedgerFilter(r *http.Request, timezone string) (profitLedgerFilter, error) {
	start, end, err := profitReportRange(r, timezone)
	if err != nil {
		return profitLedgerFilter{}, err
	}
	q := r.URL.Query()
	f := profitLedgerFilter{start: start, end: end, shop: strings.TrimSpace(q.Get("shop_key")), source: q.Get("source"), status: q.Get("status"), currency: strings.TrimSpace(q.Get("currency")), keyword: strings.TrimSpace(q.Get("q")), undated: q.Get("include_undated") == "1"}
	if f.status == "" {
		f.status = "all"
	}
	if !map[string]bool{"all": true, "matched": true, "order_unmatched": true, "sku_unattributed": true, "conflict": true, "non_order": true, "review_stale": true}[f.status] {
		return f, fmt.Errorf("关联状态无效")
	}
	if !map[string]bool{"": true, "settlement": true, "shipping": true, "return_merchant": true, "return_third_party": true, "buyer_chargeback": true, "fulfillment_delay": true, "fulfillment_false_ship": true, "platform_return": true, "disposal": true}[f.source] {
		return f, fmt.Errorf("费用来源无效")
	}
	if len([]rune(f.keyword)) > 200 || len(f.currency) > 16 || len(f.shop) > 100 {
		return f, fmt.Errorf("查询条件过长")
	}
	return f, nil
}
func (f profitLedgerFilter) args() []any {
	return []any{f.start, f.end, f.shop, f.source, f.status, f.currency, reconciliationKeyword(f.keyword), f.undated}
}

const profitLedgerWhere = `((occurred_at >= $1 AND occurred_at < $2) OR ($8 AND occurred_at IS NULL)) AND ($3='' OR shop_key=$3) AND ($4='' OR source=$4)
 AND ($5='all' OR ($5='matched' AND allocation_status IN ('matched','allocated')) OR ($5='order_unmatched' AND order_status<>'matched') OR ($5='sku_unattributed' AND allocation_status='unattributed') OR ($5='conflict' AND order_status='conflict') OR ($5='non_order' AND allocation_status='non_order') OR ($5='review_stale' AND review->>'status'='stale'))
 AND ($6='' OR currency=$6) AND ($7='' OR source_reference ILIKE $7 OR order_reference ILIKE $7 OR po_no ILIKE $7 OR package_no ILIKE $7 OR tracking_no ILIKE $7 OR platform_sku ILIKE $7 OR products::text ILIKE $7 OR package_evidence::text ILIKE $7 OR allocations::text ILIKE $7)`

type profitLedgerResponse struct {
	Rows        []json.RawMessage `json:"rows"`
	Totals      []json.RawMessage `json:"totals"`
	SKUTotals   []json.RawMessage `json:"sku_totals"`
	PackageSync []json.RawMessage `json:"package_sync"`
	Total       int64             `json:"total"`
	Page        int               `json:"page"`
	PageSize    int               `json:"page_size"`
}

func (s *APIServer) profitLedger(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	f, err := parseProfitLedgerFilter(r, s.timezone)
	if err != nil {
		writeJSON(w, 400, apiResponse{Success: false, Error: err.Error()})
		return
	}
	page := normalizedPage(queryInt(r, "page", 1), 1, 100000)
	size := normalizedPage(queryInt(r, "page_size", 30), 30, 200)
	result := profitLedgerResponse{Rows: []json.RawMessage{}, Totals: []json.RawMessage{}, PackageSync: []json.RawMessage{}, Page: page, PageSize: size}
	var payload, totals, sync, skus []byte
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.internalError(w, "begin finance ledger", err)
		return
	}
	defer tx.Rollback()
	// Derived PO counts have poor planner estimates. Keep these reporting
	// settings within this transaction to avoid repeated scans and JIT delays.
	if _, err := tx.ExecContext(ctx, `SET LOCAL jit=off; SET LOCAL enable_nestloop=off`); err != nil {
		s.internalError(w, "prepare finance ledger", err)
		return
	}
	err = tx.QueryRowContext(ctx, `WITH filtered AS MATERIALIZED(SELECT * FROM profit_reviewed_ledger_for_range($1,$2,$3,$4,$6,$8) WHERE `+profitLedgerWhere+`),
 paged AS(SELECT * FROM filtered ORDER BY occurred_at DESC NULLS LAST,source,source_id DESC LIMIT $9 OFFSET $10),
 totals AS(SELECT shop_key,currency,COUNT(*) rows,SUM(amount) amount,COUNT(*) FILTER(WHERE order_status='matched' OR (review->>'status'='active' AND allocation_status='allocated')) order_matched_rows,
 COUNT(*) FILTER(WHERE allocation_status IN ('matched','allocated')) sku_matched_rows,COALESCE(SUM(attributed_amount),0) sku_matched_amount,
 COUNT(*) FILTER(WHERE allocation_status='unattributed') unattributed_rows,COALESCE(SUM(unattributed_amount),0) unattributed_amount,COALESCE(SUM(non_order_amount),0) non_order_amount FROM filtered GROUP BY 1,2),
 sku_entries AS(SELECT entry_key,shop_key,currency,source,platform_sku,amount FROM filtered WHERE allocation_status='matched'
 UNION ALL SELECT f.entry_key,f.shop_key,f.currency,f.source,a->>'platform_sku',(a->>'amount')::numeric FROM filtered f CROSS JOIN LATERAL jsonb_array_elements(f.allocations) a WHERE allocation_status='allocated'),
 sku_totals AS(SELECT shop_key,currency,platform_sku,COUNT(DISTINCT entry_key) rows,COALESCE(SUM(amount) FILTER(WHERE source='settlement'),0) settlement_amount,COALESCE(SUM(amount) FILTER(WHERE source<>'settlement'),0) fee_amount,SUM(amount) net_amount FROM sku_entries GROUP BY 1,2,3)
 SELECT (SELECT COUNT(*) FROM filtered),COALESCE((SELECT jsonb_agg(to_jsonb(paged) ORDER BY occurred_at DESC NULLS LAST,source,source_id DESC) FROM paged),'[]'::jsonb),
 COALESCE((SELECT jsonb_agg(to_jsonb(totals) ORDER BY shop_key,currency) FROM totals),'[]'::jsonb),
 COALESCE((SELECT jsonb_agg(to_jsonb(x) ORDER BY shop_key) FROM profit_package_sync x WHERE ($3='' OR shop_key=$3)),'[]'::jsonb),
 COALESCE((SELECT jsonb_agg(to_jsonb(sku_totals) ORDER BY shop_key,currency,platform_sku) FROM sku_totals),'[]'::jsonb)`, append(f.args(), size, (page-1)*size)...).Scan(&result.Total, &payload, &totals, &sync, &skus)
	if err != nil {
		s.internalError(w, "load finance ledger", err)
		return
	}
	for _, pair := range []struct {
		raw  []byte
		dest any
	}{{payload, &result.Rows}, {totals, &result.Totals}, {sync, &result.PackageSync}, {skus, &result.SKUTotals}} {
		if err := json.Unmarshal(pair.raw, pair.dest); err != nil {
			s.internalError(w, "decode finance ledger", err)
			return
		}
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: result})
}
func (s *APIServer) exportProfitLedger(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	f, err := parseProfitLedgerFilter(r, s.timezone)
	if err != nil {
		writeJSON(w, 400, apiResponse{Success: false, Error: err.Error()})
		return
	}
	timezone := s.timezone
	if _, err := time.LoadLocation(timezone); err != nil {
		timezone = "Asia/Shanghai"
	}
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.internalError(w, "begin ledger export", err)
		return
	}
	defer tx.Rollback()
	// Derived PO counts have poor planner estimates. Keep these reporting
	// settings within this transaction to avoid repeated scans and JIT delays.
	if _, err := tx.ExecContext(ctx, `SET LOCAL jit=off; SET LOCAL enable_nestloop=off`); err != nil {
		s.internalError(w, "prepare ledger export", err)
		return
	}
	rows, err := tx.QueryContext(ctx, `SELECT entry_key,shop_key,source,source_reference,fee_name,order_reference,po_no,package_no,tracking_no,COALESCE(sku_id::text,''),platform_sku,currency,original_amount::text,amount::text,
 COALESCE(to_char(occurred_at AT TIME ZONE $9,'YYYY-MM-DD HH24:MI:SS'),''),COALESCE(to_char(paid_at AT TIME ZONE $9,'YYYY-MM-DD HH24:MI:SS'),''),COALESCE(to_char(order_at AT TIME ZONE $9,'YYYY-MM-DD HH24:MI:SS'),''),
 order_status,allocation_status,allocation_reason,products::text,array_to_string(candidate_pos,';'),package_evidence::text,remark,allocations::text,non_order_amount::text,COALESCE(review::text,'')
 FROM profit_reviewed_ledger_for_range($1,$2,$3,$4,$6,$8) WHERE `+profitLedgerWhere+` ORDER BY occurred_at DESC NULLS LAST,source,source_id DESC`, append(f.args(), timezone)...)
	if err != nil {
		s.internalError(w, "export finance ledger", err)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="temu-order-fees.csv"`)
	if _, err := w.Write([]byte("\xef\xbb\xbf")); err != nil {
		return
	}
	writer := csv.NewWriter(w)
	writer.UseCRLF = true
	headers := []string{"明细编号", "店铺", "来源", "源单据号", "费用名", "原始订单编号", "PO 单号", "费用包裹号", "费用运单号", "源 SKU ID", "归属 MSKU", "币种", "原始金额", "收支金额", "费用时间（" + timezone + "）", "到账时间（" + timezone + "）", "下单时间（" + timezone + "）", "订单关联状态", "SKU 归因状态", "未归因原因", "订单商品（含 ID、MSKU、属性、名称）", "候选 PO", "包裹关联依据", "备注", "分摊明细（含依据、金额、尾差、规则版本）", "已确认非订单净额", "人工核对记录"}
	if err := writer.Write(headers); err != nil {
		return
	}
	for rows.Next() {
		values := make([]string, len(headers))
		dest := make([]any, len(headers))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			s.logger.Error("scan ledger export", "error", err)
			return
		}
		for i := range values {
			if i != 12 && i != 13 && i != 25 {
				values[i] = profitCSVText(values[i])
			}
		}
		if err := writer.Write(values); err != nil {
			return
		}
	}
	writer.Flush()
	if err := rows.Err(); err != nil {
		s.logger.Error("read ledger export", "error", err)
	}
	if err := writer.Error(); err != nil {
		s.logger.Error("write ledger export", "error", err)
	}
}
