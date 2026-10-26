package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func ledgerFixture(t *testing.T) (*Store, context.Context) {
	store, ctx, _, _ := attributionFixture(t, 3*time.Minute)
	t.Cleanup(func() {
		for _, table := range []string{"profit_order_packages", "profit_package_sync", "temu_profit_shipping_label_fees", "temu_profit_buyer_chargebacks", "temu_profit_disposal_fees"} {
			if _, err := store.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE shop_key IN ('attribution-test','attribution-test-other')"); err != nil {
				t.Error(err)
			}
		}
	})
	links := []profitPackageLink{
		{Source: "shipment", SourceKey: "1", PO: "PO_SINGLE", Package: "PK_A", Tracking: "TR_A", Updated: time.Now()},
		{Source: "order_detail", SourceKey: "PO_SINGLE", PO: "PO_SINGLE", Child: "1", Package: "PK_A", Updated: time.Now()},
		{Source: "shipment", SourceKey: "2", PO: "PO_ID_UNKNOWN", Package: "PK_B", Tracking: "TR_B", Updated: time.Now()},
		{Source: "shipment", SourceKey: "3", PO: "PO_SINGLE", Package: "PK_M", Tracking: "TR_M", Updated: time.Now()},
		{Source: "shipment", SourceKey: "3", PO: "PO_MULTI", Package: "PK_M", Tracking: "TR_M", Updated: time.Now()},
	}
	if err := store.replaceProfitPackages(ctx, "attribution-test", links); err != nil {
		t.Fatal(err)
	}
	if err := store.replaceProfitPackages(ctx, "attribution-test-other", []profitPackageLink{{Source: "shipment", SourceKey: "4", PO: "PO_SINGLE", Package: "PK_OTHER", Tracking: "TR_OTHER", Updated: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	_, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_shipping_label_fees(shop_key,billing_phase,package_no,tracking_no,remark,freight_amount,currency,occurred_at) VALUES
 ('attribution-test','posted','PK_A','TR_A','single',-5,'USD','2026-09-15'),
 ('attribution-test','posted','','TR_A','tracking_only',-6,'USD','2026-09-15'),
 ('attribution-test','posted','PK_A','TR_B','conflict',-7,'USD','2026-09-15'),
 ('attribution-test','posted','PK_M','TR_M','merged',-8,'USD','2026-09-15'),
 ('attribution-test','posted','PK_OTHER','TR_OTHER','cross_shop',-9,'USD','2026-09-15'),
 ('attribution-test','pending','PK_A','TR_A','pending_excluded',-10,'USD',NULL),
 ('attribution-test','posted','PK_A','TR_A','undated',1,'EUR',NULL);
 INSERT INTO temu_profit_buyer_chargebacks(shop_key,violation_order_no,amount,currency,accounted_at) VALUES('attribution-test','PO_SINGLE',3,'USD','2026-09-15');
 INSERT INTO temu_profit_disposal_fees(shop_key,sku_id,tracking_no,destroy_fee,currency,accounted_at) VALUES('attribution-test',200,'RETURN',1.35,'USD','2026-09-15');`)
	if err != nil {
		t.Fatal(err)
	}
	return store, ctx
}
func TestLedgerPreservesEverySourceAmountAndKeepsAmbiguity(t *testing.T) {
	store, ctx := ledgerFixture(t)
	expected := map[string]string{"single": "matched", "tracking_only": "matched", "conflict": "conflict", "merged": "ambiguous", "cross_shop": "unmatched", "undated": "matched"}
	rows, err := store.db.QueryContext(ctx, `SELECT remark,order_status,sku_status,platform_sku,reason,jsonb_array_length(package_evidence) FROM profit_ledger WHERE shop_key='attribution-test' AND source='shipping'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var remark, order, sku, code, reason string
		var evidence int
		if err := rows.Scan(&remark, &order, &sku, &code, &reason, &evidence); err != nil {
			t.Fatal(err)
		}
		if order != expected[remark] {
			t.Errorf("%s order=%s expected=%s", remark, order, expected[remark])
		}
		if order != "matched" && (sku != "unattributed" || code != "" || reason == "") {
			t.Errorf("ambiguous assignment %s: %s %s %s", remark, sku, code, reason)
		}
		if remark == "single" && (code != "A" || evidence != 3) {
			t.Errorf("single link=%s evidence=%d", code, evidence)
		}
		delete(expected, remark)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(expected) > 0 {
		t.Fatal(expected)
	}
	var sourceCount, ledgerCount int
	var difference string
	err = store.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM profit_ledger_source WHERE shop_key='attribution-test'),COUNT(*), (SUM(amount)-(SELECT SUM(amount) FROM profit_ledger_source WHERE shop_key='attribution-test'))::text FROM profit_ledger WHERE shop_key='attribution-test'`).Scan(&sourceCount, &ledgerCount, &difference)
	if err != nil {
		t.Fatal(err)
	}
	if sourceCount != ledgerCount || difference != "0.0000" {
		t.Fatalf("source=%d ledger=%d delta=%s", sourceCount, ledgerCount, difference)
	}
	var amount, sku string
	if err := store.db.QueryRowContext(ctx, `SELECT amount::text,platform_sku FROM profit_ledger WHERE shop_key='attribution-test' AND source='disposal'`).Scan(&amount, &sku); err != nil {
		t.Fatal(err)
	}
	if amount != "-1.3500" || sku != "B" {
		t.Fatalf("disposal %s %s", amount, sku)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE normalized_order_lines l SET raw_payload='{"skuId":9999}' WHERE order_id IN(SELECT id FROM normalized_orders WHERE shop_key='attribution-test' AND source_order_no='PO_MULTI') AND platform_sku='B'; UPDATE temu_profit_disposal_fees SET sku_id=9999 WHERE shop_key='attribution-test'`); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT platform_sku FROM profit_ledger WHERE shop_key='attribution-test' AND source='disposal'`).Scan(&sku); err != nil {
		t.Fatal(err)
	}
	if sku != "B" {
		t.Fatal("root SKU identity not supported", sku)
	}
}
func TestPackageReplacementIsAtomicAndShopScoped(t *testing.T) {
	store, ctx := ledgerFixture(t)
	// An invalid snapshot must roll back the preceding delete.
	err := store.replaceProfitPackages(ctx, "attribution-test", []profitPackageLink{{Source: "shipment", SourceKey: "x", PO: "PO", Package: "", Updated: time.Now()}})
	if err == nil {
		t.Fatal("invalid link accepted")
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profit_order_packages WHERE shop_key='attribution-test'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatal("old snapshot lost", count)
	}
	if err := store.replaceProfitPackages(ctx, "attribution-test", []profitPackageLink{}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profit_order_packages WHERE shop_key='attribution-test-other'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("other shop lost", count)
	}
}
func TestLedgerAPIAndExportApplyTheSameFilters(t *testing.T) {
	store, _ := ledgerFixture(t)
	server := &APIServer{store: store, timezone: "Asia/Shanghai", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	base := "/?start=2026-09-01&end=2026-09-30&shop_key=attribution-test&source=shipping"
	w := httptest.NewRecorder()
	server.profitLedger(w, httptest.NewRequest("GET", base+"&page_size=2", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Data profitLedgerResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Total != 5 || len(result.Data.Rows) != 2 {
		t.Fatal(w.Body.String())
	}
	var skuTotals []struct {
		Amount float64 `json:"net_amount"`
	}
	rawSKUs, _ := json.Marshal(result.Data.SKUTotals)
	if err := json.Unmarshal(rawSKUs, &skuTotals); err != nil {
		t.Fatal(err)
	}
	if len(skuTotals) != 1 || skuTotals[0].Amount != -11 {
		t.Fatalf("SKU total differs from attributed ledger: %s", rawSKUs)
	}

	w = httptest.NewRecorder()
	server.exportProfitLedger(w, httptest.NewRequest("GET", base+"&page_size=2", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\xef\xbb\xbf"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 || records[1][13][0] != '-' {
		t.Fatal("export not full or numeric amounts escaped", records)
	}
	w = httptest.NewRecorder()
	server.profitLedger(w, httptest.NewRequest("GET", base+"&currency=EUR&include_undated=1", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Total != 1 {
		t.Fatal(w.Body.String())
	}
	for _, filter := range []string{"status=bad", "source=bad", "start=wrong"} {
		w = httptest.NewRecorder()
		req := httptest.NewRequest("GET", base, nil)
		bits := strings.SplitN(filter, "=", 2)
		q := req.URL.Query()
		q.Set(bits[0], bits[1])
		req.URL.RawQuery = q.Encode()
		server.profitLedger(w, req)
		if w.Code != 400 {
			t.Fatalf("%s code %d", filter, w.Code)
		}
	}
	w = httptest.NewRecorder()
	server.profitLedger(w, httptest.NewRequest("GET", base+"&q=%25", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Total != 0 {
		t.Fatal("search wildcard not escaped")
	}
}

func TestDeclaredValueAllocationConservesNegativeAmountsAndRounding(t *testing.T) {
	store, ctx := ledgerFixture(t)
	var parent int64
	if err := store.db.QueryRowContext(ctx, `INSERT INTO temu_profit_settled_parent_orders(shop_key,po_no,currency) VALUES('attribution-test','PO_MULTI','USD') RETURNING id`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_settled_parent_skus(parent_order_id,line_no,sku_id,sku_ext_code,quantity,declared_price) VALUES($1,1,100,'A',1,10),($1,2,200,'B',1,20)`, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_settled_flows(shop_key,flow_id,po_no,trade_type,settle_amount,currency,received_at) VALUES('attribution-test','ROUND','PO_MULTI','运费冲回',-0.01,'USD','2026-09-15'),('attribution-test','FRACTION','PO_MULTI','运费冲回',-0.0001,'USD','2026-09-15')`); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT source_reference,amount::text,allocation_status,allocations FROM profit_ledger_for_range('2026-09-01','2026-10-01','attribution-test','settlement','USD',false) WHERE source_reference IN ('MULTI','ROUND','FRACTION','ID') ORDER BY source_reference`)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for rows.Next() {
		var ref, amount, status string
		var payload []byte
		if err := rows.Scan(&ref, &amount, &status, &payload); err != nil {
			t.Fatal(err)
		}
		var parts []struct {
			SKU     string  `json:"platform_sku"`
			Amount  float64 `json:"amount"`
			Version int     `json:"policy_version"`
		}
		if err := json.Unmarshal(payload, &parts); err != nil {
			t.Fatal(err)
		}
		if ref == "ID" {
			if status != "matched" || len(parts) != 0 {
				t.Fatal("direct SKU changed")
			}
			continue
		}
		if status != "allocated" || len(parts) != 2 {
			t.Fatalf("%s %s %s", ref, status, payload)
		}
		found++
		expected := map[string][]float64{"MULTI": {2.33, 4.67}, "ROUND": {0, -0.01}, "FRACTION": {0, -0.0001}}[ref]
		for i, part := range parts {
			if part.Amount != expected[i] || part.Version < 1 {
				t.Fatalf("%s split %s", ref, payload)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if found != 3 {
		t.Fatal(found)
	}
	var mismatches int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profit_ledger_for_range('2026-09-01','2026-10-01','attribution-test','','',true) WHERE amount<>attributed_amount+unattributed_amount OR (allocation_status='allocated' AND amount<>(SELECT SUM((x->>'amount')::numeric) FROM jsonb_array_elements(allocations) x))`).Scan(&mismatches); err != nil {
		t.Fatal(err)
	}
	if mismatches != 0 {
		t.Fatal("allocation imbalance", mismatches)
	}
}

func TestAllocationPolicyVersioningAndAudit(t *testing.T) {
	store, ctx := ledgerFixture(t)
	server := &APIServer{store: store, timezone: "Asia/Shanghai", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var version int
	var method string
	if err := store.db.QueryRowContext(ctx, `SELECT version,method FROM profit_allocation_policy WHERE id=1`).Scan(&version, &method); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := store.db.ExecContext(ctx, `UPDATE profit_allocation_policy SET method=$1 WHERE id=1`, method); err != nil {
			t.Error(err)
		}
	})
	request := func(method string, v int, origin, header string) int {
		body := fmt.Sprintf(`{"method":%q,"version":%d}`, method, v)
		r := httptest.NewRequest("PUT", "http://finance.test/api/profit/allocation-policy", strings.NewReader(body))
		r.Header.Set("X-Pangu-Request", header)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		server.saveProfitAllocationPolicy(w, r)
		return w.Code
	}
	if code := request("quantity", version, "http://other.test", "finance"); code != 403 {
		t.Fatal(code)
	}
	if code := request("quantity", version, "http://finance.test", ""); code != 403 {
		t.Fatal(code)
	}
	if code := request("bad", version, "http://finance.test", "finance"); code != 400 {
		t.Fatal(code)
	}
	if code := request("quantity", version, "http://finance.test", "finance"); code != 200 {
		t.Fatal(code)
	}
	if code := request("manual", version, "http://finance.test", "finance"); code != 409 {
		t.Fatal(code)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profit_allocation_policy_history WHERE version=$1 AND method='quantity'`, version+1).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("missing audit")
	}
}

func TestPackageSyncFailureRetainsTheLastSuccessfulSnapshot(t *testing.T) {
	store, ctx := ledgerFixture(t)
	var before time.Time
	if err := store.db.QueryRowContext(ctx, `SELECT succeeded_at FROM profit_package_sync WHERE shop_key='attribution-test'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	unavailable := openSource("host=127.0.0.1 port=1 dbname=unavailable connect_timeout=1 sslmode=disable")
	defer unavailable.Close()
	syncer := &Syncer{store: store, temu: unavailable, forceFull: true}
	if err := syncer.syncProfitPackages(ctx, "attribution-test", "temu_panda_homes"); err == nil {
		t.Fatal("source failure hidden")
	}
	var after time.Time
	var count int
	var message string
	if err := store.db.QueryRowContext(ctx, `SELECT succeeded_at,error_message,(SELECT COUNT(*) FROM profit_order_packages WHERE shop_key='attribution-test') FROM profit_package_sync WHERE shop_key='attribution-test'`).Scan(&after, &message, &count); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) || count != 5 || message == "" {
		t.Fatalf("snapshot changed: %v %v %d %s", before, after, count, message)
	}
}
