package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type ProfitAttributionTotal struct {
	ShopKey            string  `json:"shop_key"`
	Currency           string  `json:"currency"`
	TotalRows          int64   `json:"total_rows"`
	DirectRows         int64   `json:"direct_rows"`
	MatchedRows        int64   `json:"matched_rows"`
	UnattributedRows   int64   `json:"unattributed_rows"`
	TotalAmount        float64 `json:"total_amount"`
	DirectAmount       float64 `json:"direct_amount"`
	MatchedAmount      float64 `json:"matched_amount"`
	UnattributedAmount float64 `json:"unattributed_amount"`
}

func (s *Store) queryProfitAttributionTotals(ctx context.Context, start, end time.Time, shop string) ([]ProfitAttributionTotal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT shop_key,currency,COUNT(*),COUNT(*) FILTER(WHERE attribution_method='direct'),
 COUNT(*) FILTER(WHERE attribution_method IN ('sku_id','po_single')),COUNT(*) FILTER(WHERE attribution_method='unattributed'),
 SUM(settle_amount),COALESCE(SUM(settle_amount) FILTER(WHERE attribution_method='direct'),0),
 COALESCE(SUM(settle_amount) FILTER(WHERE attribution_method IN ('sku_id','po_single')),0),
 COALESCE(SUM(settle_amount) FILTER(WHERE attribution_method='unattributed'),0)
 FROM profit_settlement_attribution WHERE received_at >= $1 AND received_at < $2 AND ($3='' OR shop_key=$3)
 GROUP BY 1,2 ORDER BY 1,2`, start, end, shop)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProfitAttributionTotal{}
	for rows.Next() {
		var row ProfitAttributionTotal
		if err := rows.Scan(&row.ShopKey, &row.Currency, &row.TotalRows, &row.DirectRows, &row.MatchedRows, &row.UnattributedRows, &row.TotalAmount, &row.DirectAmount, &row.MatchedAmount, &row.UnattributedAmount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func profitReportRange(r *http.Request, timezone string) (time.Time, time.Time, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.FixedZone("CST", 8*60*60)
	}
	now := time.Now().In(location)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location).AddDate(0, 0, 1)
	if raw := strings.TrimSpace(r.URL.Query().Get("end")); raw != "" {
		v, e := time.ParseInLocation("2006-01-02", raw, location)
		if e != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("end 参数格式应为 YYYY-MM-DD")
		}
		end = v.AddDate(0, 0, 1)
	}
	start := end.AddDate(0, 0, -30)
	if raw := strings.TrimSpace(r.URL.Query().Get("start")); raw != "" {
		v, e := time.ParseInLocation("2006-01-02", raw, location)
		if e != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("start 参数格式应为 YYYY-MM-DD")
		}
		start = v
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("start 必须早于 end")
	}
	return start, end, nil
}

type profitAttributionFilter struct {
	start, end                      time.Time
	shop, method, currency, keyword string
}

func parseProfitAttributionFilter(r *http.Request, timezone string) (profitAttributionFilter, error) {
	start, end, err := profitReportRange(r, timezone)
	if err != nil {
		return profitAttributionFilter{}, err
	}
	f := profitAttributionFilter{start: start, end: end, shop: strings.TrimSpace(r.URL.Query().Get("shop_key")), method: strings.TrimSpace(r.URL.Query().Get("status")), currency: strings.TrimSpace(r.URL.Query().Get("currency")), keyword: strings.TrimSpace(r.URL.Query().Get("q"))}
	if f.method == "" {
		f.method = "all"
	}
	if !map[string]bool{"all": true, "direct": true, "matched": true, "unattributed": true}[f.method] {
		return f, fmt.Errorf("归因状态无效")
	}
	if len([]rune(f.keyword)) > 200 || len(f.currency) > 16 {
		return f, fmt.Errorf("查询条件过长")
	}
	return f, nil
}
func (f profitAttributionFilter) args() []any {
	return []any{f.start, f.end, f.shop, f.method, f.currency, reconciliationKeyword(f.keyword)}
}

const profitAttributionWhere = `received_at >= $1 AND received_at < $2 AND ($3='' OR shop_key=$3)
 AND ($4='all' OR attribution_method=$4 OR ($4='matched' AND attribution_method IN ('sku_id','po_single')))
 AND ($5='' OR currency=$5) AND ($6='' OR flow_id ILIKE $6 OR po_no ILIKE $6 OR document_no ILIKE $6 OR platform_sku ILIKE $6 OR sku_name ILIKE $6 OR source_sku ILIKE $6)`

type ProfitAttributionResponse struct {
	Rows     []json.RawMessage `json:"rows"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

func (s *APIServer) profitSettlementAttribution(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	f, err := parseProfitAttributionFilter(r, s.timezone)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Error: err.Error()})
		return
	}
	page := normalizedPage(queryInt(r, "page", 1), 1, 100000)
	pageSize := normalizedPage(queryInt(r, "page_size", 30), 30, 200)
	args := append(f.args(), pageSize, (page-1)*pageSize)
	result := ProfitAttributionResponse{Rows: []json.RawMessage{}, Page: page, PageSize: pageSize}
	var payload []byte
	err = s.store.db.QueryRowContext(ctx, `WITH filtered AS MATERIALIZED(SELECT * FROM profit_settlement_attribution WHERE `+profitAttributionWhere+`),paged AS(SELECT * FROM filtered ORDER BY received_at DESC,id DESC LIMIT $7 OFFSET $8)
 SELECT (SELECT COUNT(*) FROM filtered),COALESCE((SELECT jsonb_agg(to_jsonb(paged) ORDER BY received_at DESC,id DESC) FROM paged),'[]'::jsonb)`, args...).Scan(&result.Total, &payload)
	if err != nil {
		s.internalError(w, "load settlement attribution", err)
		return
	}
	if err := json.Unmarshal(payload, &result.Rows); err != nil {
		s.internalError(w, "decode settlement attribution", err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: result})
}

func (s *APIServer) exportProfitSettlementAttribution(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	f, err := parseProfitAttributionFilter(r, s.timezone)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Error: err.Error()})
		return
	}
	timezone := s.timezone
	if _, err := time.LoadLocation(timezone); err != nil {
		timezone = "Asia/Shanghai"
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT shop_key,flow_id,po_no,document_no,trade_type,currency,settle_amount::text,
 COALESCE(to_char(received_at AT TIME ZONE $7,'YYYY-MM-DD HH24:MI:SS'),''),source_sku,platform_sku,sku_name,
 CASE attribution_method WHEN 'direct' THEN '原始 SKU' WHEN 'sku_id' THEN 'SKU ID 匹配' WHEN 'po_single' THEN '单 SKU PO 匹配' ELSE '未归因' END,reason
 FROM profit_settlement_attribution WHERE `+profitAttributionWhere+` ORDER BY received_at DESC,id DESC`, append(f.args(), timezone)...)
	if err != nil {
		s.internalError(w, "export settlement attribution", err)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="temu-settlement-attribution.csv"`)
	if _, err := w.Write([]byte("\xef\xbb\xbf")); err != nil {
		return
	}
	writer := csv.NewWriter(w)
	writer.UseCRLF = true
	if err := writer.Write([]string{"店铺", "结算流水 ID", "PO 单号", "单据号", "交易类型", "币种", "结算金额", "到账时间（" + timezone + "）", "原始 SKU", "归属 SKU", "商品名称", "归因方式", "未归因原因"}); err != nil {
		return
	}
	for rows.Next() {
		values := make([]string, 13)
		dest := make([]any, 13)
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			s.logger.Error("scan settlement export", "error", err)
			return
		}
		for i := range values {
			if i != 6 {
				values[i] = profitCSVText(values[i])
			}
		}
		if err := writer.Write(values); err != nil {
			return
		}
	}
	writer.Flush()
	if err := rows.Err(); err != nil {
		s.logger.Error("read settlement export", "error", err)
	}
	if err := writer.Error(); err != nil {
		s.logger.Error("write settlement export", "error", err)
	}
}
func profitCSVText(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}
