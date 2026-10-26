package main

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *APIServer) readMargin(ctx context.Context, query url.Values, export bool) ([]byte, int, error) {
	endpoint := "/api/costs/margin"
	if export {
		endpoint += "/export.csv"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(envOr("RESTOCK_API_BASE_URL", "http://127.0.0.1:18110"), "/")+endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, 503, err
	}
	response, err := (&http.Client{Timeout: 110 * time.Second}).Do(req)
	if err != nil {
		return nil, 503, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (96<<20)+1))
	if err != nil || len(data) > 96<<20 {
		return nil, 503, fmt.Errorf("毛利报告超过响应限制")
	}
	return data, response.StatusCode, nil
}
func (s *APIServer) marginReport(w http.ResponseWriter, r *http.Request) {
	export := strings.HasSuffix(r.URL.Path, "/export")
	query := r.URL.Query()
	query.Del("all")
	data, status, err := s.readMargin(r.Context(), query, export)
	if err != nil {
		writeJSON(w, 503, apiResponse{Success: false, Error: "毛利核算服务暂不可用"})
		return
	}
	if export && status == 200 {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="order-gross-margin.csv"`)
		w.Write(data)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(data)
}
func (s *APIServer) marginSnapshots(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT jsonb_build_object('id',id,'sha256',sha256,'created_at',created_at,'params',params,'status',payload::jsonb->>'status','summary',payload::jsonb->'summary') FROM profit_margin_reports ORDER BY id DESC LIMIT 100`)
	if err != nil {
		s.internalError(w, "list margin snapshots", err)
		return
	}
	defer rows.Close()
	data := []json.RawMessage{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			s.internalError(w, "scan margin snapshot", err)
			return
		}
		data = append(data, json.RawMessage(b))
	}
	if err = rows.Err(); err != nil {
		s.internalError(w, "read margin snapshots", err)
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: map[string]any{"rows": data}})
}
func (s *APIServer) saveMarginSnapshot(w http.ResponseWriter, r *http.Request) {
	if !financeWriteOK(w, r) {
		return
	}
	var input struct {
		Start  string `json:"start"`
		End    string `json:"end"`
		AsOf   string `json:"as_of"`
		Shop   string `json:"shop_key"`
		Q      string `json:"q"`
		Status string `json:"status"`
	}
	if decodeFinanceBody(w, r, &input) != nil {
		writeJSON(w, 400, apiResponse{Success: false, Error: "快照条件无效"})
		return
	}
	query := url.Values{"start": {input.Start}, "end": {input.End}, "as_of": {input.AsOf}, "shop_key": {input.Shop}, "q": {input.Q}, "status": {input.Status}, "all": {"1"}}
	data, status, err := s.readMargin(r.Context(), query, false)
	if err != nil || status != 200 {
		writeJSON(w, 503, apiResponse{Success: false, Error: "未能计算完整快照，未保存"})
		return
	}
	var envelope struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || !envelope.Success {
		writeJSON(w, 503, apiResponse{Success: false, Error: "毛利快照格式无效"})
		return
	}
	digest := sha256.Sum256(envelope.Data)
	hash := hex.EncodeToString(digest[:])
	params, _ := json.Marshal(input)
	var id int64
	err = s.store.db.QueryRowContext(r.Context(), `INSERT INTO profit_margin_reports(params,payload,sha256) VALUES($1::jsonb,$2,$3) ON CONFLICT(sha256) DO UPDATE SET sha256=EXCLUDED.sha256 RETURNING id`, string(params), string(envelope.Data), hash).Scan(&id)
	if err != nil {
		s.internalError(w, "archive margin snapshot", err)
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: map[string]any{"id": id, "sha256": hash}})
}
func (s *APIServer) marginSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeJSON(w, 400, apiResponse{Success: false, Error: "快照编号无效"})
		return
	}
	var payload, hash string
	err = s.store.db.QueryRowContext(r.Context(), `SELECT payload,sha256 FROM profit_margin_reports WHERE id=$1`, id).Scan(&payload, &hash)
	if err != nil {
		writeJSON(w, 404, apiResponse{Success: false, Error: "快照不存在"})
		return
	}
	if r.URL.Query().Get("export") == "1" {
		exportMarginSnapshot(w, []byte(payload), hash, r.URL.Query().Get("part"))
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: map[string]any{"id": id, "sha256": hash, "payload": json.RawMessage(payload)}})
}
func exportMarginSnapshot(w http.ResponseWriter, payload []byte, hash string, part string) {
	var report struct {
		Rows       []map[string]json.RawMessage `json:"rows"`
		Unassigned []map[string]json.RawMessage `json:"unassigned"`
		Overhead   []map[string]json.RawMessage `json:"overhead"`
	}
	if json.Unmarshal(payload, &report) != nil {
		writeJSON(w, 500, apiResponse{Success: false, Error: "快照格式无效"})
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="gross-margin-snapshot.csv"`)
	w.Write([]byte("\ufeff"))
	writer := csv.NewWriter(w)
	keys := []string{"order_date", "shop_key", "order_no", "platform_sku", "product_id", "site", "product_name", "variant_name", "sales_quantity", "refund_quantity", "currency", "platform_net", "costs", "gross_profit", "gross_margin", "status", "reasons", "transactions", "cost_details", "refund_details"}
	if part == "unassigned" || part == "overhead" {
		records := report.Unassigned
		if part == "overhead" {
			records = report.Overhead
		}
		keys = []string{"entry_key", "shop_key", "source", "source_reference", "fee_name", "order_no", "platform_sku", "currency", "amount", "date", "paid_at", "package_no", "tracking_no", "allocation_reason"}
		writer.Write([]string{"流水键", "店铺", "来源", "来源业务键", "费用名", "订单号", "销售SKU", "币种", "原币金额", "费用日期", "付款时间", "包裹", "运单", "归因原因", "完整明细", "快照SHA256"})
		for _, row := range records {
			cells := []string{}
			for _, key := range keys {
				value := string(row[key])
				var str string
				if json.Unmarshal(row[key], &str) == nil {
					value = str
				}
				if value == "null" {
					value = ""
				}
				cells = append(cells, financeCSVText(value))
			}
			raw, _ := json.Marshal(row)
			cells = append(cells, financeCSVText(string(raw)), hash)
			writer.Write(cells)
		}
		writer.Flush()
		return
	}
	writer.Write([]string{"订单日期", "店铺", "订单号", "销售SKU", "产品ID", "站点", "产品名称", "属性", "出库销量", "退款件数", "报告币种", "平台收支净额", "商品成本", "毛利润", "毛利率", "状态", "缺失数据", "平台款项明细", "批次成本明细", "退款明细", "快照SHA256"})
	for _, r := range report.Rows {
		cells := []string{}
		for _, key := range keys {
			raw := r[key]
			value := string(raw)
			var str string
			if json.Unmarshal(raw, &str) == nil {
				value = str
			}
			if value == "null" {
				value = ""
			}
			cells = append(cells, financeCSVText(value))
		}
		cells = append(cells, hash)
		writer.Write(cells)
	}
	writer.Flush()
}
func financeCSVText(value string) string {
	if strings.HasPrefix(strings.TrimSpace(value), "=") || strings.HasPrefix(strings.TrimSpace(value), "+") || strings.HasPrefix(strings.TrimSpace(value), "@") || (strings.HasPrefix(strings.TrimSpace(value), "-") && !marginNumber.MatchString(strings.TrimPrefix(strings.TrimSpace(value), "-"))) {
		return "'" + value
	}
	return value
}
