package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

func billSource(kind string) bool {
	return kind == ProfitFileFundDetail || kind == ProfitFileSettledFlow || kind == ProfitFileSettledParent
}

var billNumericText = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func billOriginal(raw string, fallback float64, supplied bool) (any, error) {
	if !supplied {
		raw = fmt.Sprint(fallback)
	}
	if raw == "" {
		return nil, nil
	}
	if !billNumericText.MatchString(raw) || len(raw) > 100 {
		return nil, fmt.Errorf("核对金额不是有限十进制数：%s", raw)
	}
	return raw, nil
}

// Evidence belongs to the source workbook, independently of the shared revenue
// business keys. Re-uploading an immutable file cannot promote stale evidence.
func recordBillEvidence(ctx context.Context, tx *sql.Tx, shop string, batch ProfitImportBatch) (int, bool, error) {
	if !billSource(batch.Kind) {
		return 0, false, nil
	}
	digest, basis := batch.SourceSHA256, "file"
	if digest == "" {
		raw, err := json.Marshal(batch)
		if err != nil {
			return 0, false, err
		}
		digest = fmt.Sprintf("%x", sha256.Sum256(raw))
		basis = "parsed_rows"
	}
	var fileID int64
	err := tx.QueryRowContext(ctx, `INSERT INTO profit_bill_files(shop_key,source,filename,sha256,hash_basis) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING RETURNING id`, shop, batch.Kind, batch.File, digest, basis).Scan(&fileID)
	if err == sql.ErrNoRows {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO profit_bill_rows(file_id,row_no,business_key,po_no,currency,trade_type,amount,sales_receipt,freight_receipt,sales_chargeback,freight_chargeback,discounted,direction_error,received_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`)
	if err != nil {
		return 0, false, err
	}
	defer stmt.Close()
	seen := map[string]bool{}
	count := 0
	insert := func(key, po, code, trade string, amount, sales, freight, salesBack, freightBack any, discounted, direction bool, received *time.Time, item any) error {
		if key == "" || seen[key] {
			return fmt.Errorf("核对依据的业务键为空或重复：%s", key)
		}
		seen[key] = true
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		count++
		_, err = stmt.ExecContext(ctx, fileID, count, key, po, code, trade, amount, sales, freight, salesBack, freightBack, discounted, direction, nullableTimePtr(received), raw)
		return err
	}
	if batch.Kind == ProfitFileSettledParent {
		for _, r := range batch.Parents {
			values := make([]any, 4)
			for i, k := range []string{"sales_receipt", "freight_receipt", "sales_chargeback", "freight_chargeback"} {
				value, err := billOriginal(r.OriginalAmounts[k], []float64{r.SalesReceipt, r.FreightReceipt, r.SalesChargeback, r.FreightChargeback}[i], r.OriginalAmounts != nil)
				if err != nil {
					return 0, false, err
				}
				values[i] = value
			}
			if err := insert(r.PONo, r.PONo, r.Currency, "", nil, values[0], values[1], values[2], values[3], r.SalesReceiptAfterDiscount != 0 || r.FreightReceiptAfterDiscount != 0, r.SalesReceipt < 0 || r.FreightReceipt < 0, nil, r); err != nil {
				return 0, false, err
			}
		}
	} else {
		for _, r := range batch.Settled {
			amount, err := billOriginal(r.OriginalAmount, r.SettleAmount, r.OriginalAmount != "")
			if err != nil {
				return 0, false, err
			}
			direction := (r.TradeType == "销售回款" || r.TradeType == "运费回款") && r.SettleAmount < 0 || (r.TradeType == "销售冲回" || r.TradeType == "运费冲回") && r.SettleAmount > 0
			if err := insert(r.FlowID, r.PONo, r.Currency, r.TradeType, amount, nil, nil, nil, nil, false, direction, r.ReceivedAt, r); err != nil {
				return 0, false, err
			}
		}
	}
	return count, false, nil
}

func (s *Store) importBillEvidence(ctx context.Context, shop string, batches []ProfitImportBatch) (map[string]any, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('profit_import'),hashtext($1))`, shop); err != nil {
		return nil, err
	}
	rows, files, unchanged := 0, 0, 0
	for _, b := range batches {
		if !billSource(b.Kind) {
			return nil, fmt.Errorf("仅核对依据导入只接受账务明细、PO 明细和 PO 聚合账单：%s", b.File)
		}
		count, duplicate, err := recordBillEvidence(ctx, tx, shop, b)
		if err != nil {
			return nil, fmt.Errorf("%s：%w", b.File, err)
		}
		rows += count
		if duplicate {
			unchanged++
		} else {
			files++
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"files": files, "rows": rows, "unchanged_files": unchanged, "mode": "reconciliation_only"}, nil
}

type billFilter struct {
	Shop, Currency, Status, Q string
	Page, Size                int
}

func parseBillFilter(r *http.Request) (billFilter, error) {
	q := r.URL.Query()
	f := billFilter{Shop: strings.TrimSpace(q.Get("shop_key")), Currency: strings.TrimSpace(q.Get("currency")), Status: strings.TrimSpace(q.Get("status")), Q: strings.TrimSpace(q.Get("q")), Page: normalizedPage(queryInt(r, "page", 1), 1, 100000), Size: normalizedPage(queryInt(r, "page_size", 30), 30, 200)}
	if !map[string]bool{"": true, "matched": true, "difference": true, "missing": true, "review": true}[f.Status] || len(f.Currency) > 16 || len([]rune(f.Q)) > 200 {
		return f, fmt.Errorf("状态、币种或查询条件无效")
	}
	return f, nil
}

type billComparisonResult struct {
	Rows     []json.RawMessage `json:"rows"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
	Summary  json.RawMessage   `json:"summary"`
	Files    json.RawMessage   `json:"files"`
}

const billFiltered = `WITH filtered AS MATERIALIZED(SELECT * FROM profit_bill_comparison WHERE ($1='' OR shop_key=$1) AND ($2='' OR currency=$2) AND ($3='' OR status=$3) AND ($4='' OR po_no ILIKE $4))`

func (s *Store) queryBillComparison(ctx context.Context, f billFilter, export bool) (billComparisonResult, error) {
	result := billComparisonResult{Rows: []json.RawMessage{}, Page: f.Page, PageSize: f.Size}
	var raw []byte
	paging := "LIMIT $5 OFFSET $6"
	if export {
		paging = ""
	}
	args := []any{f.Shop, f.Currency, f.Status, reconciliationKeyword(f.Q)}
	if !export {
		args = append(args, f.Size, (f.Page-1)*f.Size)
	}
	err := s.db.QueryRowContext(ctx, billFiltered+`,paged AS (SELECT * FROM filtered ORDER BY shop_key,po_no,currency `+paging+`)
 SELECT (SELECT count(*) FROM filtered),COALESCE((SELECT jsonb_agg(to_jsonb(paged) ORDER BY shop_key,po_no,currency) FROM paged),'[]'::jsonb),
 (SELECT jsonb_build_object('matched',count(*) FILTER(WHERE status='matched'),'difference',count(*) FILTER(WHERE status='difference'),'missing',count(*) FILTER(WHERE status='missing'),'review',count(*) FILTER(WHERE status='review')) FROM filtered),
 COALESCE((SELECT jsonb_agg(to_jsonb(v) ORDER BY imported_at DESC,id DESC) FROM (SELECT f.*,count(r.row_no) AS rows FROM profit_bill_files f LEFT JOIN profit_bill_rows r ON r.file_id=f.id WHERE ($1='' OR f.shop_key=$1) GROUP BY f.id ORDER BY f.imported_at DESC,f.id DESC LIMIT 30) v),'[]'::jsonb)
 `, args...).Scan(&result.Total, &raw, &result.Summary, &result.Files)
	if err == nil {
		err = json.Unmarshal(raw, &result.Rows)
	}
	return result, err
}
func (s *APIServer) billComparison(w http.ResponseWriter, r *http.Request) {
	f, err := parseBillFilter(r)
	if err != nil {
		writeJSON(w, 400, apiResponse{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	export := r.URL.Query().Get("export") == "1"
	result, err := s.store.queryBillComparison(ctx, f, export)
	if err != nil {
		s.internalError(w, "load bill comparison", err)
		return
	}
	if export {
		writeBillCSV(w, result.Rows)
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: result})
}
func (s *APIServer) billComparisonDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	shop, po, code := strings.TrimSpace(q.Get("shop_key")), q.Get("po_no"), q.Get("currency")
	if shop == "" || len([]rune(po)) > 200 || len(code) > 16 {
		writeJSON(w, 400, apiResponse{Success: false, Error: "店铺、PO 或币种无效"})
		return
	}
	page := normalizedPage(queryInt(r, "page", 1), 1, 100000)
	fileID := queryInt(r, "file_id", 0)
	if fileID < 0 {
		writeJSON(w, 400, apiResponse{Success: false, Error: "依据文件编号无效"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var total int64
	var raw []byte
	source := "profit_bill_current_rows"
	if fileID > 0 {
		source = "(SELECT f.shop_key,f.source,f.filename,f.sha256,f.hash_basis,f.imported_at,r.* FROM profit_bill_files f JOIN profit_bill_rows r ON r.file_id=f.id) evidence"
	}
	err := s.store.db.QueryRowContext(ctx, `WITH selected AS MATERIALIZED(SELECT *,amount::text AS original_amount,sales_receipt::text AS original_sales_receipt,freight_receipt::text AS original_freight_receipt,sales_chargeback::text AS original_sales_chargeback,freight_chargeback::text AS original_freight_chargeback FROM `+source+` WHERE shop_key=$1 AND (($5>0 AND file_id=$5) OR ($5=0 AND po_no=$2 AND currency=$3))), paged AS(SELECT * FROM selected ORDER BY source,business_key LIMIT 100 OFFSET $4) SELECT (SELECT count(*) FROM selected),COALESCE((SELECT jsonb_agg(to_jsonb(paged) ORDER BY source,business_key) FROM paged),'[]'::jsonb)`, shop, po, code, (page-1)*100, fileID).Scan(&total, &raw)
	if err != nil {
		s.internalError(w, "load bill evidence", err)
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: map[string]any{"rows": json.RawMessage(raw), "total": total, "page": page, "page_size": 100}})
}

var billCSVNumber = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)

func writeBillCSV(w http.ResponseWriter, rows []json.RawMessage) {
	components := []string{"sales_receipt", "freight_receipt", "sales_chargeback", "freight_chargeback"}
	names := []string{"销售回款", "运费回款", "销售冲回", "运费冲回"}
	headers := []string{"店铺", "PO", "币种", "状态", "核对原因", "流水差异数"}
	for _, source := range []string{"账务明细", "PO明细", "PO聚合", "账务明细减PO明细", "PO明细减PO聚合"} {
		for _, name := range names {
			headers = append(headers, source+"-"+name)
		}
	}
	headers = append(headers, "来源文件与SHA256")
	records := [][]string{headers}
	for _, raw := range rows {
		var row map[string]any
		_ = json.Unmarshal(raw, &row)
		str := func(v any) string {
			if v == nil {
				return ""
			}
			return fmt.Sprint(v)
		}
		reasons, _ := json.Marshal(row["reasons"])
		record := []string{str(row["shop_key"]), str(row["po_no"]), str(row["currency"]), str(row["status"]), string(reasons), str(row["flow_differences"])}
		provenance := map[string]any{}
		for _, key := range []string{"fund", "detail", "parent"} {
			source, _ := row[key].(map[string]any)
			for _, c := range components {
				record = append(record, str(source[c]))
			}
			provenance[key] = source["files"]
		}
		deltas, _ := row["deltas"].(map[string]any)
		for _, key := range []string{"fund_detail", "detail_parent"} {
			delta, _ := deltas[key].(map[string]any)
			for _, c := range components {
				record = append(record, str(delta[c]))
			}
		}
		files, _ := json.Marshal(provenance)
		record = append(record, string(files))
		for i, value := range record {
			trim := strings.TrimSpace(value)
			if strings.HasPrefix(trim, "=") || strings.HasPrefix(trim, "+") || strings.HasPrefix(trim, "-") || strings.HasPrefix(trim, "@") {
				if !billCSVNumber.MatchString(value) {
					record[i] = "'" + value
				}
			}
		}
		records = append(records, record)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="temu-bill-comparison.csv"`)
	_, _ = w.Write([]byte("\xef\xbb\xbf"))
	writer := csv.NewWriter(w)
	_ = writer.WriteAll(records)
}
