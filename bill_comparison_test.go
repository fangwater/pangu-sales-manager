package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

func billTestStore(t *testing.T, shop string) (context.Context, *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	s := openTestProfitStore(t, ctx)
	if s == nil {
		return ctx, nil
	}
	clean := func() {
		for _, query := range []string{`DELETE FROM profit_bill_rows WHERE file_id IN(SELECT id FROM profit_bill_files WHERE shop_key=$1)`, `DELETE FROM profit_bill_files WHERE shop_key=$1`, `DELETE FROM temu_profit_settled_flows WHERE shop_key=$1`, `DELETE FROM temu_profit_settled_parent_orders WHERE shop_key=$1`} {
			if _, err := s.db.ExecContext(ctx, query, shop); err != nil {
				t.Error(err)
			}
		}
	}
	clean()
	t.Cleanup(func() { clean(); s.Close() })
	return ctx, s
}
func billFlow(key, po, kind string, amount float64) TemuProfitSettledFlow {
	return TemuProfitSettledFlow{FlowID: key, PONo: po, TradeType: kind, SettleAmount: amount, Currency: "USD"}
}
func billBatch(kind string, rows ...TemuProfitSettledFlow) ProfitImportBatch {
	return ProfitImportBatch{Kind: kind, File: kind + ".xlsx", Settled: rows}
}
func billParent(po string, sales, freight, salesBack, freightBack float64) ProfitImportBatch {
	return ProfitImportBatch{Kind: ProfitFileSettledParent, File: "parent.xlsx", Parents: []TemuProfitSettledParentOrder{{PONo: po, Currency: "USD", SalesReceipt: sales, FreightReceipt: freight, SalesChargeback: salesBack, FreightChargeback: freightBack}}}
}
func billRead(t *testing.T, ctx context.Context, s *Store, shop string) map[string]map[string]any {
	t.Helper()
	d, err := s.queryBillComparison(ctx, billFilter{Shop: shop, Page: 1, Size: 200}, false)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]map[string]any{}
	for _, raw := range d.Rows {
		var r map[string]any
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		result[r["po_no"].(string)+"/"+r["currency"].(string)] = r
	}
	return result
}
func billImport(t *testing.T, ctx context.Context, s *Store, shop string, batches ...ProfitImportBatch) map[string]any {
	t.Helper()
	r, err := s.importBillEvidence(ctx, shop, batches)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBillComparisonUsesIndependentSourcesAndExactComponents(t *testing.T) {
	const shop = "bill-independent"
	ctx, s := billTestStore(t, shop)
	if s == nil {
		return
	}
	flows := []TemuProfitSettledFlow{billFlow("S", "P", "销售回款", 10.0001), billFlow("F", "P", "运费回款", 2), billFlow("SC", "P", "销售冲回", -1.1234), billFlow("FC", "P", "运费冲回", -0.0001)}
	for _, kind := range []string{ProfitFileFundDetail, ProfitFileSettledFlow} {
		if _, err := s.applyProfitImport(ctx, shop, []ProfitImportBatch{billBatch(kind, flows...)}); err != nil {
			t.Fatal(err)
		}
	}
	billImport(t, ctx, s, shop, billParent("P", 10.0001, 2, 1.1234, 0.0001))
	r := billRead(t, ctx, s, shop)["P/USD"]
	if r["status"] != "matched" {
		t.Fatalf("not reconciled: %v", r)
	}
	if r["fund"].(map[string]any)["sales_receipt"] != "10.0001" {
		t.Fatalf("precision lost: %v", r)
	}
	var count int
	var amount string
	if err := s.db.QueryRowContext(ctx, `SELECT count(*),sum(settle_amount)::text FROM temu_profit_settled_flows WHERE shop_key=$1`, shop).Scan(&count, &amount); err != nil {
		t.Fatal(err)
	}
	if count != 4 || amount != "10.8766" {
		t.Fatalf("sources double counted: %d %s", count, amount)
	}
	// Updating the shared canonical flow must not fabricate changed source evidence.
	if _, err := s.db.ExecContext(ctx, `UPDATE temu_profit_settled_flows SET settle_amount=999 WHERE shop_key=$1 AND flow_id='S'`, shop); err != nil {
		t.Fatal(err)
	}
	if billRead(t, ctx, s, shop)["P/USD"]["status"] != "matched" {
		t.Fatal("canonical union substituted for provenance")
	}
}
func TestBillComparisonRejectsNetOnlyAndTransactionOnlyMatches(t *testing.T) {
	const shop = "bill-components"
	ctx, s := billTestStore(t, shop)
	if s == nil {
		return
	}
	fund := []TemuProfitSettledFlow{billFlow("A", "OFFSET", "销售回款", 10), billFlow("B", "OFFSET", "销售冲回", -5), billFlow("C", "IDS", "销售回款", 8)}
	detail := []TemuProfitSettledFlow{billFlow("A", "OFFSET", "销售回款", 12), billFlow("B", "OFFSET", "销售冲回", -7), billFlow("DIFFERENT-ID", "IDS", "销售回款", 8)}
	billImport(t, ctx, s, shop, billBatch(ProfitFileFundDetail, fund...), billBatch(ProfitFileSettledFlow, detail...), billParent("OFFSET", 12, 0, 7, 0), billParent("IDS", 8, 0, 0, 0))
	all := billRead(t, ctx, s, shop)
	for _, po := range []string{"OFFSET/USD", "IDS/USD"} {
		if all[po]["status"] != "difference" {
			t.Fatalf("false match: %v", all[po])
		}
	}
	delta := all["OFFSET/USD"]["deltas"].(map[string]any)["fund_detail"].(map[string]any)
	if delta["sales_receipt"] != "-2" || delta["sales_chargeback"] != "2" {
		t.Fatalf("component differences missing: %v", delta)
	}
	if all["IDS/USD"]["flow_differences"] != float64(2) {
		t.Fatalf("IDs aggregated away: %v", all["IDS/USD"])
	}
}
func TestBillComparisonSeparatesPrecisionDifferencesWithoutDroppingRawAmounts(t *testing.T) {
	const shop = "bill-precision"
	ctx, s := billTestStore(t, shop)
	if s == nil {
		return
	}
	a := billFlow("F", "TAIL", "销售回款", 1.23)
	a.OriginalAmount = "1.2300000000000001"
	b := billFlow("B", "BOUNDARY", "销售回款", 1.23456)
	b.OriginalAmount = "1.23456"
	c := a
	c.OriginalAmount = "1.23"
	d := b
	d.OriginalAmount = "1.23454"
	billImport(t, ctx, s, shop, billBatch(ProfitFileFundDetail, a, b), billBatch(ProfitFileSettledFlow, c, d), billParent("TAIL", 1.23, 0, 0, 0), billParent("BOUNDARY", 1.23454, 0, 0, 0))
	rows := billRead(t, ctx, s, shop)
	if r := rows["TAIL/USD"]; r["status"] != "precision" || r["fund"].(map[string]any)["sales_receipt"] != "1.2300000000000001" || r["flow_differences"] != float64(1) || r["accounting_flow_differences"] != float64(0) {
		t.Fatalf("precision evidence lost: %v", r)
	}
	if rows["BOUNDARY/USD"]["status"] != "difference" {
		t.Fatal("rounding boundary masked by absolute tolerance")
	}
}

func TestBillComparisonKeepsMissingSourcesNullAndCurrencyShopSeparate(t *testing.T) {
	const shop = "bill-missing"
	ctx, s := billTestStore(t, shop)
	if s == nil {
		return
	}
	if _, err := s.applyProfitImport(ctx, shop, []ProfitImportBatch{{Settled: []TemuProfitSettledFlow{billFlow("OLD", "LEGACY", "销售回款", 0)}}}); err != nil {
		t.Fatal(err)
	}
	usd := billFlow("U", "CURRENCY", "销售回款", 7)
	eur := usd
	eur.Currency = "EUR"
	billImport(t, ctx, s, shop, billBatch(ProfitFileFundDetail, usd), billBatch(ProfitFileSettledFlow, eur), billParent("ZERO", 0, 0, 0, 0))
	all := billRead(t, ctx, s, shop)
	if r := all["LEGACY/USD"]; r["status"] != "missing" || r["fund"] != nil || r["detail"] != nil || r["parent"] != nil {
		t.Fatalf("legacy falsely attributed: %v", r)
	}
	if r := all["ZERO/USD"]; r["status"] != "missing" || r["fund"] != nil || r["parent"].(map[string]any)["sales_receipt"] != "0" {
		t.Fatalf("zero confused with absent: %v", r)
	}
	for _, code := range []string{"USD", "EUR"} {
		if all["CURRENCY/"+code]["status"] != "review" {
			t.Fatal("currencies combined")
		}
	}
	other := billRead(t, ctx, s, "other-store")
	if len(other) != 0 {
		t.Fatal("cross-shop evidence")
	}
}
func TestBillEvidenceIsImmutableIdempotentAndAtomic(t *testing.T) {
	const shop = "bill-atomic"
	ctx, s := billTestStore(t, shop)
	if s == nil {
		return
	}
	old := billBatch(ProfitFileSettledFlow, billFlow("KEY", "P", "销售回款", 1))
	newer := billBatch(ProfitFileSettledFlow, billFlow("KEY", "P", "销售回款", 2))
	billImport(t, ctx, s, shop, old)
	billImport(t, ctx, s, shop, newer)
	r := billImport(t, ctx, s, shop, old)
	if r["files"] != 0 || r["unchanged_files"] != 1 {
		t.Fatalf("repeat not skipped: %v", r)
	}
	if billRead(t, ctx, s, shop)["P/USD"]["detail"].(map[string]any)["sales_receipt"] != "2" {
		t.Fatal("old file promoted")
	}
	var oldID int64
	if err := s.db.QueryRowContext(ctx, `SELECT min(id) FROM profit_bill_files WHERE shop_key=$1`, shop).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	server := &APIServer{store: s, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	request := httptest.NewRequest("GET", fmt.Sprintf("/api/profit/bill-comparison/detail?shop_key=%s&file_id=%d", shop, oldID), nil)
	w := httptest.NewRecorder()
	server.billComparisonDetail(w, request)
	var historical struct {
		Data struct {
			Rows []struct {
				Amount string `json:"original_amount"`
			} `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &historical); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(historical.Data.Rows) != 1 || historical.Data.Rows[0].Amount != "1" {
		t.Fatalf("historical evidence changed: %s", w.Body.String())
	}
	var files int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM profit_bill_files WHERE shop_key=$1`, shop).Scan(&files)
	bad := billBatch(ProfitFileSettledFlow, billFlow("DUP", "D", "销售回款", 1), billFlow("DUP", "D", "销售回款", 2))
	if _, err := s.importBillEvidence(ctx, shop, []ProfitImportBatch{billParent("ROLLBACK", 1, 0, 0, 0), bad}); err == nil {
		t.Fatal("duplicate row accepted")
	}
	var after int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM profit_bill_files WHERE shop_key=$1`, shop).Scan(&after)
	if after != files {
		t.Fatal("partial evidence committed")
	}
	if _, err := s.applyProfitImport(ctx, shop, []ProfitImportBatch{bad}); err == nil {
		t.Fatal("normal import should also reject ambiguous source")
	}
	var raw int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM temu_profit_settled_flows WHERE shop_key=$1`, shop).Scan(&raw)
	if raw != 0 {
		t.Fatal("evidence-only wrote finance or normal failure partially committed")
	}
	if _, err := s.importBillEvidence(ctx, shop, []ProfitImportBatch{billParent("ROLLBACK", 1, 0, 0, 0), {Kind: ProfitFileShippingPosted}}); err == nil {
		t.Fatal("other source accepted in evidence-only batch")
	}
	if _, exists := billRead(t, ctx, s, shop)["ROLLBACK/USD"]; exists {
		t.Fatal("mixed-file import partially committed")
	}
}
func TestBillComparisonRetainsUncertainBusinessBasis(t *testing.T) {
	const shop = "bill-review"
	ctx, s := billTestStore(t, shop)
	if s == nil {
		return
	}
	bad := []TemuProfitSettledFlow{billFlow("UNKNOWN", "UNKNOWN", "其他", 1), billFlow("DIRECTION", "DIRECTION", "销售冲回", 3), billFlow("NOPO", "", "销售回款", 1)}
	bad[2].Currency = ""
	discounted := billParent("DISCOUNT", 5, 0, 0, 0)
	discounted.Parents[0].SalesReceiptAfterDiscount = 4
	billImport(t, ctx, s, shop, billBatch(ProfitFileFundDetail, bad...), discounted)
	for _, r := range billRead(t, ctx, s, shop) {
		if r["status"] != "review" {
			t.Fatalf("uncertainty hidden: %v", r)
		}
	}
}
func TestBillSourceAmountsRemainExactAndBlankDoesNotBecomeZero(t *testing.T) {
	const shop = "bill-exact-source"
	ctx, s := billTestStore(t, shop)
	if s == nil {
		return
	}
	rows := [][]string{{"结算流水ID", "批次号", "PO单号", "单据号", "交易类型", "结算金额"}, {"EXACT", "B", "EXACT-PO", "D", "销售回款", "999999999999.1234567891", "", "", "", "", "", "", "USD"}}
	flows, err := parseSettledFlows(rows, shop, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	billImport(t, ctx, s, shop, billBatch(ProfitFileFundDetail, flows...))
	if billRead(t, ctx, s, shop)["EXACT-PO/USD"]["fund"].(map[string]any)["sales_receipt"] != "999999999999.1234567891" {
		t.Fatal("source passed through binary float")
	}
	parent, err := parseSettledParentOrderRow([]string{"BLANK-PO", "", "", "", "", "", "", "USD", "1", "0", "", "0", "0", "0"}, shop)
	if err != nil {
		t.Fatal(err)
	}
	billImport(t, ctx, s, shop, ProfitImportBatch{Kind: ProfitFileSettledParent, Parents: []TemuProfitSettledParentOrder{parent}})
	r := billRead(t, ctx, s, shop)["BLANK-PO/USD"]
	if r["status"] != "review" || r["parent"].(map[string]any)["sales_chargeback"] != nil {
		t.Fatalf("blank turned into zero: %v", r)
	}
	bad := billFlow("NAN", "NAN", "销售回款", 0)
	bad.OriginalAmount = "NaN"
	if _, err := s.importBillEvidence(ctx, shop, []ProfitImportBatch{billBatch(ProfitFileFundDetail, bad)}); err == nil {
		t.Fatal("non-finite source accepted")
	}
}

func TestBillComparisonFiltersPaginationCSVAndReadOnly(t *testing.T) {
	const shop = "bill-export"
	ctx, s := billTestStore(t, shop)
	if s == nil {
		return
	}
	billImport(t, ctx, s, shop, billParent("A_1%", 1.1234, 0, 0, 0), billParent("A21X", 8, 0, 0, 0), billParent("=FORMULA", 3, 0, 0, 0))
	d, err := s.queryBillComparison(ctx, billFilter{Shop: shop, Page: 1, Size: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.Total != 3 || len(d.Rows) != 1 {
		t.Fatalf("bad pagination: %+v", d)
	}
	d, err = s.queryBillComparison(ctx, billFilter{Shop: shop, Q: "A_1%", Page: 1, Size: 30}, false)
	if err != nil || d.Total != 1 {
		t.Fatalf("wildcard not escaped: %+v %v", d, err)
	}
	d, err = s.queryBillComparison(ctx, billFilter{Shop: shop, Page: 2, Size: 1}, true)
	if err != nil || len(d.Rows) != 3 {
		t.Fatalf("export paginated: %+v %v", d, err)
	}
	w := httptest.NewRecorder()
	writeBillCSV(w, d.Rows)
	parsed, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 4 || !strings.Contains(w.Body.String(), "'=FORMULA") || !strings.Contains(w.Body.String(), "1.1234") {
		t.Fatal("CSV precision or protection missing")
	}
	// Evidence-only import and all reporting leave legacy import runs untouched.
	var raw int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM temu_profit_settled_parent_orders WHERE shop_key=$1`, shop).Scan(&raw); err != nil || raw != 0 {
		t.Fatal("report wrote finance")
	}
}
func TestBillEvidenceHTTPPreservesFileHashAndRequiresSameOrigin(t *testing.T) {
	const shop = "bill-http"
	ctx, s := billTestStore(t, shop)
	if s == nil {
		return
	}
	book := excelize.NewFile()
	defer book.Close()
	headers := []any{"结算流水ID", "批次号", "PO单号", "单据号", "交易类型", "结算金额"}
	_ = book.SetSheetRow("Sheet1", "A1", &headers)
	row := []any{"HTTP-FLOW", "B", "HTTP-PO", "D", "销售回款", 1.1234, nil, nil, nil, nil, nil, nil, "USD"}
	_ = book.SetSheetRow("Sheet1", "A2", &row)
	style, err := book.NewStyle(&excelize.Style{NumFmt: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := book.SetCellStyle("Sheet1", "F2", "F2", style); err != nil {
		t.Fatal(err)
	}
	xlsx, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	server := &APIServer{store: s, timezone: "Asia/Shanghai", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	request := func(header, origin string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		m := multipart.NewWriter(&body)
		_ = m.WriteField("shop_key", shop)
		part, _ := m.CreateFormFile("file", "SettledFlow.xlsx")
		_, _ = part.Write(xlsx.Bytes())
		m.Close()
		r := httptest.NewRequest("POST", "/api/profit/bill-evidence/import", &body)
		r.Header.Set("Content-Type", m.FormDataContentType())
		r.Header.Set("X-Pangu-Request", header)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		server.importProfit(w, r)
		return w
	}
	if request("", "").Code != 403 || request("finance", "https://other.example").Code != 403 {
		t.Fatal("origin/header not enforced")
	}
	w := request("finance", "")
	if w.Code != 200 {
		t.Fatalf("evidence upload: %d %s", w.Code, w.Body.String())
	}
	var digest, basis string
	if err := s.db.QueryRowContext(ctx, `SELECT sha256,hash_basis FROM profit_bill_files WHERE shop_key=$1`, shop).Scan(&digest, &basis); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(xlsx.Bytes())
	if digest != fmt.Sprintf("%x", expected) || basis != "file" {
		t.Fatal("original file SHA missing")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/profit/bill-comparison?shop_key="+shop, nil)
	w = httptest.NewRecorder()
	server.billComparison(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "1.1234") {
		t.Fatal(w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/profit/bill-comparison/detail?shop_key="+shop+"&po_no=HTTP-PO&currency=USD", nil)
	w = httptest.NewRecorder()
	server.billComparisonDetail(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "HTTP-FLOW") {
		t.Fatal(w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/profit/bill-comparison?status=invalid", nil)
	w = httptest.NewRecorder()
	server.billComparison(w, r)
	if w.Code != 400 {
		t.Fatal("invalid status accepted")
	}
	var financeRows int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM temu_profit_settled_flows WHERE shop_key=$1`, shop).Scan(&financeRows)
	if financeRows != 0 {
		t.Fatal("evidence upload modified revenue")
	}
}

func TestBillParentEvidenceRetainsRawAmountsUnderDisplayFormatting(t *testing.T) {
	book := excelize.NewFile()
	defer book.Close()
	headers := []any{"PO单号", "商品信息"}
	_ = book.SetSheetRow("Sheet1", "A1", &headers)
	row := []any{"RAW-PO", nil, nil, nil, nil, nil, nil, "USD", 1.1234, 0.004, nil, 0, 0, 0}
	_ = book.SetSheetRow("Sheet1", "A2", &row)
	style, err := book.NewStyle(&excelize.Style{NumFmt: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := book.SetCellStyle("Sheet1", "I2", "N2", style); err != nil {
		t.Fatal(err)
	}
	xlsx, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := parseProfitWorkbook("parent.xlsx", xlsx.Bytes(), profitParseOptions{ShopKey: "test", Hint: ProfitFileSettledParent, Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Parents) != 1 {
		t.Fatal("missing parent")
	}
	r := batch.Parents[0]
	if r.SalesReceipt != 1.12 || r.OriginalAmounts["sales_receipt"] != "1.1234" || r.OriginalAmounts["sales_receipt_after_discount"] != "0.004" || r.OriginalAmounts["sales_chargeback"] != "" {
		t.Fatalf("display formatting affected evidence: %+v", r)
	}
}
