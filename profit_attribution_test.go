package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func attributionFixture(t *testing.T) (*Store, context.Context, time.Time, time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	store := openTestProfitStore(t, ctx)
	if store == nil {
		t.Skip("isolated database required")
	}
	t.Cleanup(func() { store.Close() })
	const shop = "attribution-test"
	t.Cleanup(func() {
		for _, table := range []string{"temu_profit_settled_flows", "temu_profit_settled_parent_orders", "normalized_orders"} {
			if _, err := store.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE shop_key IN ($1,$2)", shop, shop+"-other"); err != nil {
				t.Error(err)
			}
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM canonical_skus WHERE warehouse_sku IN ('ATTR-A','ATTR-B','ATTR-C')`); err != nil {
			t.Error(err)
		}
	})
	if _, err := store.db.ExecContext(ctx, `INSERT INTO canonical_skus(warehouse_sku) VALUES('ATTR-A'),('ATTR-B'),('ATTR-C')`); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	addOrder := func(owner, po string, codes []string, ids []int64) {
		var id int64
		if err := store.db.QueryRowContext(ctx, `INSERT INTO normalized_orders(platform,shop_key,source_order_no,occurred_at,occurred_at_source) VALUES('temu',$1,$2,$3,'test') RETURNING id`, owner, po, start).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for index, code := range codes {
			if _, err := store.db.ExecContext(ctx, `INSERT INTO normalized_order_lines(order_id,source_line_key,platform_sku,warehouse_sku,quantity,warehouse_quantity,platform_product_sku_id) VALUES($1,$2,$3,'ATTR-A',1,1,NULLIF($4,0))`, id, index+1, code, ids[index]); err != nil {
				t.Fatal(err)
			}
		}
	}
	addOrder(shop, "PO_SINGLE", []string{"A"}, []int64{100})
	addOrder(shop, "PO_ID_UNKNOWN", []string{"A"}, []int64{100})
	addOrder(shop, "PO_MULTI", []string{"A", "B"}, []int64{100, 200})
	addOrder(shop, "PO_INCOMPLETE", []string{"A", ""}, []int64{100, 0})
	addOrder(shop, "PO_CONFLICT", []string{"A"}, []int64{100})
	addOrder(shop, "ID_AMBIG", []string{"A", "C"}, []int64{300, 300})
	addOrder(shop+"-other", "PO_OTHER", []string{"A"}, []int64{100})
	_, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_settled_flows(shop_key,flow_id,po_no,trade_type,settle_amount,sku_ext_code,sku_id,currency,received_at) VALUES
 ($1,'DIRECT','PO_SINGLE','销售回款',20,'A',100,'USD',$2),
 ($1,'PO','PO_SINGLE','运费回款',3,'',NULL,'USD',$2),($1,'NEGATIVE','PO_SINGLE','运费冲回',-1,'',NULL,'USD',$2),
 ($1,'MULTI','PO_MULTI','运费回款',7,'',NULL,'USD',$2),($1,'ID','PO_MULTI','销售回款',4,'',200,'USD',$2),
 ($1,'UNKNOWN_ID','PO_ID_UNKNOWN','销售冲回',8,'',999,'USD',$2),($1,'MISSING','PO_OTHER','运费回款',9,'',NULL,'USD',$2),
 ($1,'BLANK','','运费回款',10,'',NULL,'USD',$2),($1,'AMBIG','','销售回款',11,'',300,'USD',$2),
 ($1,'INCOMPLETE','PO_INCOMPLETE','运费回款',12,'',NULL,'USD',$2),
 ($1,'CONFLICT_SEED','PO_CONFLICT','销售回款',15,'C',NULL,'USD',$2),($1,'CONFLICT','PO_CONFLICT','运费回款',2,'',NULL,'USD',$2),
 ($1,'PARENT','PO_PARENT','运费回款',6,'',NULL,'USD',$2),($1,'EUR','PO_SINGLE','运费回款',5,'',NULL,'EUR',$2),
 ($1,'BOUNDARY','PO_SINGLE','销售回款',99,'',NULL,'USD',$3)`, shop, start, end)
	if err != nil {
		t.Fatal(err)
	}
	var parent int64
	if err := store.db.QueryRowContext(ctx, `INSERT INTO temu_profit_settled_parent_orders(shop_key,po_no) VALUES($1,'PO_PARENT') RETURNING id`, shop).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_settled_parent_skus(parent_order_id,line_no,sku_id,sku_ext_code,quantity,declared_price) VALUES($1,1,200,'B',1,1)`, parent); err != nil {
		t.Fatal(err)
	}
	return store, ctx, start, end
}

func TestSettlementAttributionUsesUniqueEvidenceAndConservesAllMoney(t *testing.T) {
	store, ctx, start, end := attributionFixture(t)
	expected := map[string]string{"DIRECT": "direct", "PO": "po_single", "NEGATIVE": "po_single", "MULTI": "unattributed", "ID": "sku_id", "UNKNOWN_ID": "unattributed", "MISSING": "unattributed", "BLANK": "unattributed", "AMBIG": "unattributed", "INCOMPLETE": "unattributed", "CONFLICT_SEED": "direct", "CONFLICT": "unattributed", "PARENT": "po_single", "EUR": "po_single", "BOUNDARY": "po_single"}
	rows, err := store.db.QueryContext(ctx, `SELECT flow_id,attribution_method,platform_sku,reason FROM profit_settlement_attribution WHERE shop_key='attribution-test'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var flow, method, sku, reason string
		if err := rows.Scan(&flow, &method, &sku, &reason); err != nil {
			t.Fatal(err)
		}
		if method != expected[flow] {
			t.Fatalf("%s = %s (%s), expected %s", flow, method, reason, expected[flow])
		}
		if method == "unattributed" && (sku != "" || reason == "") {
			t.Fatalf("ambiguous row allocated: %s %s %s", flow, sku, reason)
		}
		delete(expected, flow)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(expected) != 0 {
		t.Fatal(expected)
	}
	totals, err := store.queryProfitAttributionTotals(ctx, start, end, "attribution-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 2 {
		t.Fatal(totals)
	}
	for _, total := range totals {
		if total.TotalAmount != total.DirectAmount+total.MatchedAmount+total.UnattributedAmount || total.TotalRows != total.DirectRows+total.MatchedRows+total.UnattributedRows {
			t.Fatalf("not conserved: %+v", total)
		}
		if total.Currency == "USD" && (total.TotalAmount != 106 || total.DirectAmount != 35 || total.MatchedAmount != 12 || total.UnattributedAmount != 59 || total.UnattributedRows != 7) {
			t.Fatalf("USD totals or boundary wrong: %+v", total)
		}
		if total.Currency == "EUR" && (total.TotalAmount != 5 || total.TotalRows != 1) {
			t.Fatal(total)
		}
	}
	for currency, want := range map[string]float64{"USD": 47, "EUR": 5} {
		report, err := store.profitSKUSummaryCurrency(ctx, "UTC", start, end, "attribution-test", currency)
		if err != nil {
			t.Fatal(err)
		}
		var amount float64
		for _, row := range report.Rows {
			amount += row.PaybackAmount
		}
		if amount != want || report.Currency != currency {
			t.Fatalf("SKU currencies mixed: %s %v", currency, amount)
		}
	}
	var rawCode string
	if err := store.db.QueryRowContext(ctx, `SELECT sku_ext_code FROM temu_profit_settled_flows WHERE shop_key='attribution-test' AND flow_id='PO'`).Scan(&rawCode); err != nil {
		t.Fatal(err)
	}
	if rawCode != "" {
		t.Fatal("source SKU overwritten")
	}
}

func TestSettlementAttributionQueryAndExportUseSameFilters(t *testing.T) {
	store, _, _, _ := attributionFixture(t)
	server := &APIServer{store: store, timezone: "UTC", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	query := "?shop_key=attribution-test&start=2026-09-01&end=2026-09-30&status=matched&currency=USD&q=PO_SINGLE&page_size=1"
	w := httptest.NewRecorder()
	server.profitSettlementAttribution(w, httptest.NewRequest(http.MethodGet, "/api/profit/settlement-attribution"+query, nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var response struct {
		Data ProfitAttributionResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Total != 2 || len(response.Data.Rows) != 1 {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	server.exportProfitSettlementAttribution(w, httptest.NewRequest(http.MethodGet, "/api/profit/settlement-attribution/export"+query, nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("export truncated to one page: %v", records)
	}
	var negative bool
	for _, record := range records[1:] {
		if record[6] == "-1.0000" {
			negative = true
		}
		if record[9] != "A" || record[12] != "" {
			t.Fatal(record)
		}
	}
	if !negative {
		t.Fatal("refund sign lost")
	}
	for _, suffix := range []string{"?start=2026-10-01&end=2026-09-01", "?start=invalid", "?status=direct%7Cmatched"} {
		w = httptest.NewRecorder()
		server.profitSettlementAttribution(w, httptest.NewRequest(http.MethodGet, "/api/profit/settlement-attribution"+suffix, nil))
		if w.Code != 400 {
			t.Fatal(w.Code, suffix)
		}
	}
	if profitCSVText(" =2+2") != "' =2+2" || profitCSVText("PO-123") != "PO-123" {
		t.Fatal("CSV text escaped incorrectly")
	}
}
