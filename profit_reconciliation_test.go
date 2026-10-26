package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShippingReconciliationConservesPartialAmountsAndIgnoresRemarkDifferences(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()
	const shop = "shipping-reconcile-test"
	defer store.db.ExecContext(ctx, `DELETE FROM temu_profit_shipping_label_fees WHERE shop_key=$1`, shop)
	_, err := store.db.ExecContext(ctx, `
	 INSERT INTO temu_profit_shipping_label_fees(shop_key,billing_phase,package_no,tracking_no,bill_type,remark,freight_amount,currency,reconcile_status) VALUES
	 ($1,'pending','FULL','T1','支出','',-4,'USD',''),($1,'posted','FULL','T1','支出','发货面单费（全段）',-4,'USD','支出成功'),
	 ($1,'pending','PART','T2','支出','A',-3,'USD',''),($1,'pending','PART','T2','支出','B',-5,'USD',''),
	 ($1,'posted','PART','T2','支出','已出账一',-2,'USD','支出成功'),($1,'posted','PART','T2','支出','已出账二',-1,'USD','支出成功'),
	 ($1,'posted','PART','T2','支出','退款',1,'USD','退款成功'),
	 ($1,'pending','CURRENCY','T3','支出','',-7,'USD',''),($1,'posted','CURRENCY','T3','支出','',-7,'EUR','支出成功'),
	 ($1,'pending','FAILED','T4','支出','',-9,'USD',''),($1,'posted','FAILED','T4','支出','',-9,'USD','支出失败'),
	 ($1,'pending','','','支出','',-11,'USD',''),($1,'posted','','','支出','',-11,'USD','支出成功')`, shop)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT reference,status,raw_amount,matched_amount,remaining_amount,source_rows FROM profit_pending_shipping_reconciliation WHERE shop_key=$1`, shop)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var ref, status string
		var raw, matched, remaining float64
		var sources int
		if err := rows.Scan(&ref, &status, &raw, &matched, &remaining, &sources); err != nil {
			t.Fatal(err)
		}
		if raw != matched+remaining {
			t.Fatalf("amount not conserved: %s %v %v %v", ref, raw, matched, remaining)
		}
		switch ref {
		case "FULL":
			if status != "settled" || remaining != 0 {
				t.Fatalf("remark prevented full reconciliation: %s %v", status, remaining)
			}
		case "PART":
			if status != "partial" || raw != -8 || matched != -3 || remaining != -5 || sources != 2 {
				t.Fatalf("duplicate or opposite-direction allocation: %s %v %v %v", status, raw, matched, remaining)
			}
		case "CURRENCY", "FAILED":
			if status != "pending" || matched != 0 {
				t.Fatalf("unconfirmed or foreign-currency posting used: %s", ref)
			}
		case "":
			if status != "review" || matched != 0 {
				t.Fatal("blank identifiers matched")
			}
		}
		seen[ref] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 5 {
		t.Fatalf("groups missing: %v", seen)
	}
}

func TestOrderReconciliationUsesComponentsAndParentOnlyAsFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()
	const shop = "orders-reconcile-test"
	defer func() {
		for _, table := range []string{"temu_profit_unsettle_orders", "temu_profit_settled_flows", "temu_profit_settled_parent_orders"} {
			store.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE shop_key=$1", shop)
		}
	}()
	_, err := store.db.ExecContext(ctx, `
	 INSERT INTO temu_profit_unsettle_orders(shop_key,po_no,currency,sales_receipt,freight_receipt,sales_chargeback) VALUES
	 ($1,'FULL','USD',100,10,0),($1,'PART','USD',100,10,5),($1,'ZERO-NET','USD',100,0,100),
	 ($1,'PARENT','USD',20,0,0),($1,'DISCOUNT','USD',30,0,0),($1,'FOREIGN','USD',40,0,0);
	 `, shop)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.ExecContext(ctx, `UPDATE temu_profit_unsettle_orders SET sales_receipt_after_discount=25 WHERE shop_key=$1 AND po_no='DISCOUNT'`, shop)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO temu_profit_settled_flows(shop_key,flow_id,po_no,currency,trade_type,settle_amount) VALUES
	 ($1,'F1','FULL','USD','销售回款',100),($1,'F2','FULL','USD','运费回款',10),($1,'F3','FULL','USD','销售冲回',-20),
	 ($1,'P1','PART','USD','销售回款',35),($1,'Z1','ZERO-NET','USD','销售回款',30),($1,'Z2','ZERO-NET','USD','销售冲回',-30),
	 ($1,'D1','DISCOUNT','USD','销售回款',30),($1,'E1','FOREIGN','EUR','销售回款',40)`, shop)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO temu_profit_settled_parent_orders(shop_key,po_no,currency,sales_receipt,freight_receipt) VALUES
	 ($1,'FULL','USD',100,10),($1,'PARENT','USD',20,0)`, shop)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT reference,status,remaining_amount,remaining_sales_receipt,remaining_sales_chargeback FROM profit_pending_order_reconciliation WHERE shop_key=$1`, shop)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var ref, status string
		var remaining, sales, chargeback float64
		if err := rows.Scan(&ref, &status, &remaining, &sales, &chargeback); err != nil {
			t.Fatal(err)
		}
		switch ref {
		case "FULL", "PARENT":
			if status != "settled" || remaining != 0 {
				t.Fatalf("fully posted components not closed: %s %s %v", ref, status, remaining)
			}
		case "PART":
			if status != "partial" || remaining != 70 || sales != 65 || chargeback != -5 {
				t.Fatalf("partial components: %s %v %v %v", status, remaining, sales, chargeback)
			}
		case "ZERO-NET":
			if status != "partial" || remaining != 0 || sales != 70 || chargeback != -70 {
				t.Fatal("net zero incorrectly treated as settled")
			}
		case "DISCOUNT":
			if status != "review" || remaining != 30 {
				t.Fatal("unknown discounted semantics used")
			}
		case "FOREIGN":
			if status != "pending" || remaining != 40 {
				t.Fatal("currencies combined")
			}
		}
		seen[ref] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 6 {
		t.Fatal(seen)
	}
}

func TestPendingSnapshotsArchiveOnlyTheirSourceAndRollbackOnFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()
	const shop = "snapshot-reconcile-test"
	defer func() {
		for _, table := range []string{"temu_profit_unsettle_orders", "temu_profit_shipping_label_fees"} {
			store.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE shop_key=$1", shop)
		}
	}()
	makeLabel := func(pkg string, amount float64) TemuProfitShippingLabelFee {
		return TemuProfitShippingLabelFee{BillingPhase: "pending", PackageNo: pkg, TrackingNo: pkg, FreightAmount: amount, Currency: "USD"}
	}
	first := []ProfitImportBatch{{Kind: ProfitFileShippingPending, Shipping: []TemuProfitShippingLabelFee{makeLabel("OLD", -4), makeLabel("CURRENT", -5)}}, {Kind: ProfitFileUnsettle, Unsettles: []TemuProfitUnsettleOrder{{PONo: "PO-OLD", Currency: "USD", SalesReceipt: 10}}}}
	if _, err := store.applyProfitImport(ctx, shop, first); err != nil {
		t.Fatal(err)
	}
	next := []ProfitImportBatch{{Kind: ProfitFileShippingPending, Shipping: []TemuProfitShippingLabelFee{makeLabel("CURRENT", -6)}}}
	if _, err := store.applyProfitImportMode(ctx, shop, next, "snapshot"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.applyProfitImportMode(ctx, shop, next, "snapshot"); err != nil {
		t.Fatal(err)
	}
	checks, err := store.queryProfitPendingChecks(ctx, shop)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Orders != 1 || checks[0].RawLabelRows != 2 || checks[0].LabelRows != 1 || checks[0].LabelAmount != -6 || checks[0].ArchivedLabelRows != 1 {
		t.Fatalf("snapshot scope/idempotence: %+v", checks)
	}
	broken := []ProfitImportBatch{{Kind: ProfitFileShippingPending, Shipping: []TemuProfitShippingLabelFee{{BillingPhase: "invalid", PackageNo: "BAD", TrackingNo: "BAD"}}}}
	if _, err := store.applyProfitImportMode(ctx, shop, broken, "snapshot"); err == nil {
		t.Fatal("bad import succeeded")
	}
	checks, err = store.queryProfitPendingChecks(ctx, shop)
	if err != nil || checks[0].LabelAmount != -6 {
		t.Fatalf("failed snapshot changed active data: %+v %v", checks, err)
	}
}

func TestReconciliationEndpointFiltersAndPaginatesWithoutChangingSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()
	const shop = "reconcile-api-test"
	defer store.db.ExecContext(ctx, `DELETE FROM temu_profit_unsettle_orders WHERE shop_key=$1`, shop)
	_, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_unsettle_orders(shop_key,po_no,currency,sales_receipt) VALUES($1,'PO_1','USD',1),($1,'POX1','USD',2)`, shop)
	if err != nil {
		t.Fatal(err)
	}
	server := &APIServer{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w := httptest.NewRecorder()
	server.profitReconciliation(w, httptest.NewRequest(http.MethodGet, "/api/profit/reconciliation?kind=orders&shop_key="+shop+"&q=PO_&page_size=1", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var response struct {
		Data ProfitReconciliationResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Total != 1 || len(response.Data.Rows) != 1 {
		t.Fatalf("search wildcard or pagination: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	server.profitReconciliation(w, httptest.NewRequest(http.MethodGet, "/api/profit/reconciliation?kind=unknown", nil))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	server.profitReconciliation(w, httptest.NewRequest(http.MethodGet, "/api/profit/reconciliation?kind=orders&status=all%7Copen", nil))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestPendingSnapshotUploadKeepsSourceAndArchivesMissingOrders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()
	const shop = "snapshot-upload-test"
	defer func() {
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_unsettle_orders WHERE shop_key=$1`, shop)
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_import_runs WHERE shop_key=$1`, shop)
	}()
	if _, err := store.applyProfitImport(ctx, shop, []ProfitImportBatch{{Kind: ProfitFileUnsettle, Unsettles: []TemuProfitUnsettleOrder{{PONo: "OLD", Currency: "USD", SalesReceipt: 100}}}}); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{"shop_key": shop, "table": ProfitFileUnsettle, "pending_mode": "snapshot"} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", "pending.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(buildUnsettleWorkbook(t)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/profit/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	server := &APIServer{store: store, timezone: "Asia/Shanghai", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	response := httptest.NewRecorder()
	server.importProfit(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var result struct {
		Data ProfitImportResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.PendingMode != "snapshot" {
		t.Fatal("upload mode not returned")
	}
	var count, active, skus int
	var raw float64
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE pending_active),SUM(sales_receipt) FROM temu_profit_unsettle_orders WHERE shop_key=$1`, shop).Scan(&count, &active, &raw)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || active != 1 || raw != 112 {
		t.Fatalf("snapshot lost evidence: %d %d %v", count, active, raw)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM temu_profit_unsettle_skus sk JOIN temu_profit_unsettle_orders o ON o.id=sk.unsettle_order_id WHERE o.shop_key=$1 AND o.pending_active`, shop).Scan(&skus); err != nil {
		t.Fatal(err)
	}
	if skus != 2 {
		t.Fatalf("snapshot SKU rows: %d", skus)
	}
}
