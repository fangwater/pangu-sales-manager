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

// Warehouse evidence remains raw until the business confirms the source timezone.
func (s *APIServer) warehouseCostEvidence(w http.ResponseWriter, r *http.Request) {
	if s.syncer == nil || s.syncer.xlwms == nil {
		writeJSON(w, 503, apiResponse{Success: false, Error: "仓储流水数据源未配置"})
		return
	}
	start, end, err := profitReportRange(r, s.timezone)
	if err != nil || end.Sub(start) > 1462*24*time.Hour {
		writeJSON(w, 400, apiResponse{Success: false, Error: "流水查询日期无效或超过四年"})
		return
	}
	kind := r.URL.Query().Get("kind")
	sourceKind := ""
	if kind == "receipt" {
		sourceKind = "入库单"
	} else if kind == "movement" {
		sourceKind = "出库单"
	} else if kind != "" {
		writeJSON(w, 400, apiResponse{Success: false, Error: "流水类型无效"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	var raw, bounds []byte
	sourceTx, err := s.syncer.xlwms.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		s.internalError(w, "begin warehouse evidence", err)
		return
	}
	defer sourceTx.Rollback()
	if _, err = sourceTx.ExecContext(ctx, `SET LOCAL jit=off; SET LOCAL enable_nestloop=off`); err != nil {
		s.internalError(w, "prepare warehouse evidence", err)
		return
	}
	var count int
	err = sourceTx.QueryRowContext(ctx, `SELECT count(*) FROM(SELECT 1 FROM xlwms_inventory_records WHERE inventory_kind='stock_flow' AND relate_order_type_name IN ('入库单','出库单') AND ($3='' OR relate_order_type_name=$3) AND operate_time >= $1::date AND operate_time < $2::date LIMIT 5001)f`, start.Format("2006-01-02"), end.Format("2006-01-02"), sourceKind).Scan(&count)
	if err != nil {
		s.internalError(w, "count warehouse evidence", err)
		return
	}
	if count > 5000 {
		writeJSON(w, 413, apiResponse{Success: false, Error: "仓储流水超过 5000 行，请缩小日期范围或选择单一流水类型"})
		return
	}
	err = sourceTx.QueryRowContext(ctx, `WITH flow AS MATERIALIZED(SELECT * FROM xlwms_inventory_records
 WHERE inventory_kind='stock_flow' AND relate_order_type_name IN ('入库单','出库单')
 AND ($3='' OR relate_order_type_name=$3) AND operate_time >= $1::date AND operate_time < $2::date ORDER BY operate_time,record_key LIMIT 50001),
 documents AS MATERIALIZED(SELECT DISTINCT wh_code,relate_order_no FROM flow WHERE relate_order_type_name='出库单'),
 refs AS MATERIALIZED(SELECT x.wh_code,x.outbound_order_no,jsonb_agg(DISTINCT ref) refs FROM xlwms_outbound_order_index x
 JOIN documents d ON d.wh_code=x.wh_code AND d.relate_order_no=x.outbound_order_no
 CROSS JOIN LATERAL unnest(ARRAY[x.third_order_no,x.refer_order_no,x.platform_order_no]) ref
 WHERE ref IS NOT NULL AND ref<>'' GROUP BY x.wh_code,x.outbound_order_no)
 SELECT COALESCE(jsonb_agg(jsonb_build_object('source_key',r.record_key,'warehouse_code',r.wh_code,'warehouse_sku',r.sku,'document_no',r.relate_order_no,'kind',r.relate_order_type_name,
 'change_quantity',r.change_amount::text,'raw_time',r.operate_time::text,'batch_no',r.batch_no,'last_seen_at',r.last_seen_at,'references',COALESCE(x.refs,'[]'::jsonb)) ORDER BY r.operate_time,r.record_key),'[]'::jsonb)
 FROM flow r LEFT JOIN refs x ON x.wh_code=r.wh_code AND x.outbound_order_no=r.relate_order_no`, start.Format("2006-01-02"), end.Format("2006-01-02"), sourceKind).Scan(&raw)
	if err != nil {
		s.internalError(w, "read warehouse cost evidence", err)
		return
	}
	var records []map[string]any
	if err = json.Unmarshal(raw, &records); err != nil {
		s.internalError(w, "decode warehouse evidence", err)
		return
	}
	if len(records) > 50000 {
		writeJSON(w, 413, apiResponse{Success: false, Error: "仓储流水超过 50000 行，请缩小日期范围"})
		return
	}
	err = sourceTx.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]'::jsonb) FROM(SELECT wh_code warehouse_code,relate_order_type_name kind,COUNT(*) records,MIN(operate_time)::text first_raw_time,MAX(operate_time)::text last_raw_time,MAX(last_seen_at) last_collected_at FROM xlwms_inventory_records WHERE inventory_kind='stock_flow' AND relate_order_type_name IN ('入库单','出库单') GROUP BY 1,2 ORDER BY 1,2)x`).Scan(&bounds)
	if err != nil {
		s.internalError(w, "read warehouse evidence coverage", err)
		return
	}
	var matches []byte
	err = s.store.db.QueryRowContext(ctx, `WITH source AS MATERIALIZED(SELECT * FROM jsonb_to_recordset($1::jsonb) AS s(source_key text,warehouse_sku text,"references" jsonb)),
 tokens AS MATERIALIZED(SELECT s.source_key,s.warehouse_sku,jsonb_array_elements_text(s."references") reference FROM source s),
 candidates AS(SELECT t.source_key,o.platform,o.shop_key,o.source_order_no order_no,l.platform_sku,l.conversion_factor::text conversion_factor FROM tokens t JOIN normalized_orders o ON o.source_order_no=t.reference JOIN normalized_order_lines l ON l.order_id=o.id AND l.warehouse_sku=t.warehouse_sku
 UNION SELECT t.source_key,o.platform,o.shop_key,o.source_order_no,l.platform_sku,l.conversion_factor::text FROM tokens t JOIN normalized_order_lines l ON l.source_line_key=t.reference AND l.warehouse_sku=t.warehouse_sku JOIN normalized_orders o ON o.id=l.order_id)
 SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY source_key,shop_key,order_no,platform_sku),'[]'::jsonb) FROM candidates c`, string(raw)).Scan(&matches)
	if err != nil {
		s.internalError(w, "match warehouse movements", err)
		return
	}
	payload := map[string]any{"version": 1, "start": start.Format("2006-01-02"), "end": end.AddDate(0, 0, -1).Format("2006-01-02"), "kind": kind, "source_time_kind": "timestamp_without_timezone", "records": json.RawMessage(raw), "candidates": json.RawMessage(matches), "coverage": json.RawMessage(bounds)}
	bytes, _ := json.Marshal(payload)
	digest := sha256.Sum256(bytes)
	payload["sha256"] = hex.EncodeToString(digest[:])
	writeJSON(w, 200, apiResponse{Success: true, Data: payload})
}
