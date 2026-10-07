package main

import (
	"context"
	"testing"
	"time"
)

func TestReportStatusPreservesPartialSettlementAndCurrencies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()
	const shop = "report-status-test"
	cleanup := func() {
		for _, table := range []string{"temu_profit_unsettle_orders", "temu_profit_settled_flows", "temu_profit_shipping_label_fees"} {
			if _, err := store.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE shop_key=$1", shop); err != nil {
				t.Error(err)
			}
		}
	}
	cleanup()
	defer cleanup()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_unsettle_orders(shop_key,po_no,currency,sales_receipt,sales_chargeback,freight_receipt)
	 VALUES($1,'PARTIAL','USD',100,5,10),($1,'OTHER-CURRENCY','EUR',30,0,0),($1,'DISCOUNT','USD',20,0,0)`, shop); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE temu_profit_unsettle_orders SET sales_receipt_after_discount=18 WHERE shop_key=$1 AND po_no='DISCOUNT'`, shop); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_settled_flows(shop_key,flow_id,po_no,trade_type,currency,settle_amount,received_at)
	 VALUES($1,'PARTIAL-FLOW','PARTIAL','销售回款','USD',35,now())`, shop); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_shipping_label_fees(shop_key,billing_phase,package_no,tracking_no,bill_type,freight_amount,currency)
	 VALUES($1,'pending','PKG','TRACK','支出',-4,'USD'),($1,'posted','PKG','TRACK','支出',-4,'USD')`, shop); err != nil {
		t.Fatal(err)
	}
	result, err := store.profitReportStatus(ctx, "Asia/Shanghai", shop)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pending) != 2 {
		t.Fatalf("currency groups = %+v", result.Pending)
	}
	for _, p := range result.Pending {
		if p.Currency == "USD" && (p.Orders != 2 || p.NetAmount != 125 || p.DiscountedRows != 1 || p.OverlapOrders != 1 || p.OverlapNetAmount != 105 || p.PostedOverlapRows != 1 || p.PostedOverlapAmount != -4) {
			t.Fatalf("overlap check must preserve pending amounts: %+v", p)
		}
		if p.Currency == "EUR" && (p.Orders != 1 || p.NetAmount != 30 || p.OverlapOrders != 0) {
			t.Fatalf("currency isolation: %+v", p)
		}
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM temu_profit_unsettle_orders WHERE shop_key=$1`, shop).Scan(&count); err != nil || count != 3 {
		t.Fatalf("read-only report changed pending rows: %d %v", count, err)
	}
	empty, err := store.profitReportStatus(ctx, "UTC", "report-empty-shop")
	if err != nil || len(empty.Sources) != 0 || len(empty.Pending) != 0 {
		t.Fatalf("empty status = %+v: %v", empty, err)
	}
}

func TestLedgerBalanceNormalizesExpenseSignAndIncludesDisposal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()
	const shop = "ledger-sign-test"
	cleanup := func() {
		for _, table := range []string{"temu_profit_fulfillment_violations", "temu_profit_disposal_fees", "temu_profit_platform_return_label_fees"} {
			store.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE shop_key=$1", shop)
		}
	}
	cleanup()
	defer cleanup()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_fulfillment_violations(shop_key,source_sheet,violation_no,amount,accounted_at)
	 VALUES($1,'fulfillment_delay','POSITIVE-EXPENSE',5,now()),($1,'fulfillment_delay','NEGATIVE-EXPENSE',-7,now())`, shop); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_disposal_fees(shop_key,sku_id,tracking_no,destroy_fee,accounted_at) VALUES($1,1,'DISPOSE',3,now())`, shop); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO temu_profit_platform_return_label_fees(shop_key,order_no,amount,accounted_at) VALUES($1,'COMPENSATION',4,now())`, shop); err != nil {
		t.Fatal(err)
	}
	result, err := store.profitDailySummary(ctx, "UTC", "day", shop)
	if err != nil {
		t.Fatal(err)
	}
	if result.Totals.KnownFeeBalanceAmount != -11 || result.Totals.PlatformBalanceAmount != -11 || result.Totals.DisposalFeeAmount != -3 || result.Totals.FeeRows != 4 {
		t.Fatalf("expected -5-7-3+4 = -11, got %+v", result.Totals)
	}
	if result.Totals.FulfillmentDelayAmount != -12 {
		t.Fatalf("expenses must both reduce the balance: %+v", result.Totals)
	}
	var original float64
	if err := store.db.QueryRowContext(ctx, `SELECT SUM(amount) FROM temu_profit_fulfillment_violations WHERE shop_key=$1`, shop).Scan(&original); err != nil || original != -2 {
		t.Fatalf("source values changed: %v %v", original, err)
	}
}
