package main

import (
	"context"
	"testing"
	"time"
)

func TestProfitDailySummaryAggregatesAndNetsChargebacks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()

	const shopKey = "profit-summary-test"
	const warehouseSKU = "TEST-PROFIT-SUMMARY-SKU"
	cleanup := func() {
		store.db.ExecContext(ctx, `DELETE FROM normalized_orders WHERE shop_key=$1`, shopKey)
		store.db.ExecContext(ctx, `DELETE FROM canonical_skus WHERE warehouse_sku=$1`, warehouseSKU)
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_settled_flows WHERE shop_key=$1`, shopKey)
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_buyer_chargebacks WHERE shop_key=$1`, shopKey)
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_fulfillment_violations WHERE shop_key=$1`, shopKey)
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_shipping_label_fees WHERE shop_key=$1`, shopKey)
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_return_label_fees WHERE shop_key=$1`, shopKey)
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_platform_return_label_fees WHERE shop_key=$1`, shopKey)
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_disposal_fees WHERE shop_key=$1`, shopKey)
	}
	cleanup()
	defer cleanup()

	now := time.Now().UTC()

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO canonical_skus (warehouse_sku) VALUES ($1)
	`, warehouseSKU); err != nil {
		t.Fatal(err)
	}

	var soldOrderID int64
	if err := store.db.QueryRowContext(ctx, `
		INSERT INTO normalized_orders
			(platform, shop_key, source_order_no, occurred_at, occurred_at_source, sales_eligible)
		VALUES ('temu', $1, 'PS-SOLD-1', $2, 'test', true)
		RETURNING id
	`, shopKey, now).Scan(&soldOrderID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO normalized_order_lines
			(order_id, source_line_key, platform_sku, warehouse_sku, quantity, warehouse_quantity, unit_price)
		VALUES ($1, 'L1', 'PS1', $2, 3, 3, 12.5)
	`, soldOrderID, warehouseSKU); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO normalized_orders
			(platform, shop_key, source_order_no, occurred_at, occurred_at_source, sales_eligible)
		VALUES ('temu', $1, 'PS-CANCELLED-1', $2, 'test', false)
	`, shopKey, now); err != nil {
		t.Fatal(err)
	}

	flows := []struct {
		flowID    string
		tradeType string
		amount    float64
	}{
		{"PS-FLOW-SALES", "销售回款", 100},
		{"PS-FLOW-SALES-CHARGEBACK", "销售冲回", -20},
		{"PS-FLOW-FREIGHT", "运费回款", 50},
		{"PS-FLOW-FREIGHT-CHARGEBACK", "运费冲回", -5},
	}
	for _, f := range flows {
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO temu_profit_settled_flows (shop_key, flow_id, trade_type, settle_amount, received_at)
			VALUES ($1, $2, $3, $4, $5)
		`, shopKey, f.flowID, f.tradeType, f.amount, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO temu_profit_buyer_chargebacks (shop_key, violation_order_no, amount, accounted_at)
		VALUES ($1, 'PS-VIOLATION-1', -10, $2)
	`, shopKey, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO temu_profit_fulfillment_violations (shop_key, source_sheet, violation_no, amount, accounted_at)
		VALUES ($1, 'fulfillment_delay', 'PS-VIO-DELAY-1', -3, $2), ($1, 'fulfillment_false_ship', 'PS-VIO-SHIP-1', -7, $2)
	`, shopKey, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO temu_profit_shipping_label_fees
			(shop_key, billing_phase, package_no, tracking_no, bill_type, freight_amount, occurred_at)
		VALUES ($1, 'posted', 'PS-PKG-1', 'PS-TRK-1', '支出', -4, $2)
	`, shopKey, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO temu_profit_return_label_fees
			(shop_key, return_destination, fund_bill_id, amount, occurred_at)
		VALUES ($1, 'merchant_warehouse', 'PS-FUND-1', -2, $2), ($1, 'third_party_warehouse', 'PS-FUND-2', -6, $2)
	`, shopKey, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO temu_profit_platform_return_label_fees (shop_key, order_no, amount, accounted_at)
		VALUES ($1, 'PS-ORDER-1', -1, $2)
	`, shopKey, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO temu_profit_disposal_fees (shop_key,sku_id,tracking_no,destroy_fee,accounted_at)
		VALUES ($1,555,'PS-DISPOSAL-1',8,$2)
	`, shopKey, now); err != nil {
		t.Fatal(err)
	}

	result, err := store.profitDailySummary(ctx, "UTC", "day", shopKey)
	if err != nil {
		t.Fatal(err)
	}
	totals := result.Totals
	if totals.Units != 3 {
		t.Fatalf("units = %v, want 3", totals.Units)
	}
	if totals.RefundOrders != 1 {
		t.Fatalf("refund orders = %v, want 1", totals.RefundOrders)
	}
	if totals.SalesChargebacks != 1 || totals.FreightChargebacks != 1 {
		t.Fatalf("chargebacks = sales:%d freight:%d, want 1/1", totals.SalesChargebacks, totals.FreightChargebacks)
	}
	if totals.BuyerChargebacks != 1 {
		t.Fatalf("buyer chargebacks = %v, want 1", totals.BuyerChargebacks)
	}
	if totals.RefundTotal != 4 {
		t.Fatalf("refund total = %v, want 4 (1 cancelled + 1 sales chargeback + 1 freight chargeback + 1 buyer chargeback)", totals.RefundTotal)
	}
	wantPayback := 100.0 - 20 + 50 - 5
	if totals.PaybackAmount != wantPayback {
		t.Fatalf("payback amount = %v, want %v", totals.PaybackAmount, wantPayback)
	}
	if totals.SalesReceiptAmount != 100 || totals.FreightReceiptAmount != 50 {
		t.Fatalf("receipts = sales:%v freight:%v, want 100/50", totals.SalesReceiptAmount, totals.FreightReceiptAmount)
	}
	if totals.SalesChargebackAmount != -20 || totals.FreightChargebackAmount != -5 {
		t.Fatalf("chargeback amounts = sales:%v freight:%v, want -20/-5", totals.SalesChargebackAmount, totals.FreightChargebackAmount)
	}
	if totals.BuyerChargebackAmount != -10 {
		t.Fatalf("buyer chargeback amount = %v, want -10", totals.BuyerChargebackAmount)
	}
	if totals.FulfillmentDelayAmount != -3 || totals.FulfillmentFalseShipAmount != -7 {
		t.Fatalf("violation amounts = delay:%v falseShip:%v, want -3/-7", totals.FulfillmentDelayAmount, totals.FulfillmentFalseShipAmount)
	}
	if totals.ShippingLabelFeeAmount != -4 {
		t.Fatalf("shipping label fee amount = %v, want -4", totals.ShippingLabelFeeAmount)
	}
	if totals.ReturnLabelFeeMerchantAmount != -2 || totals.ReturnLabelFeeThirdPartyAmount != -6 {
		t.Fatalf("return label fee amounts = merchant:%v thirdParty:%v, want -2/-6", totals.ReturnLabelFeeMerchantAmount, totals.ReturnLabelFeeThirdPartyAmount)
	}
	if totals.PlatformReturnLabelFeeAmount != -1 {
		t.Fatalf("platform return label fee amount = %v, want -1", totals.PlatformReturnLabelFeeAmount)
	}
	if totals.TaxWithheldAmount != nil || totals.TaxRefundAmount != nil || totals.NonOrderTransactionFeeAmount != nil {
		t.Fatalf("tax/non-order fee columns should stay nil (no data source), got %+v", totals)
	}
	if totals.DisposalFeeAmount != -8 || totals.KnownFeeBalanceAmount != -41 || totals.PlatformBalanceAmount != 84 {
		t.Fatalf("known ledger balance = %+v, want disposal -8, fees -41 and platform balance 84", totals)
	}
	if totals.SettledRows != 4 || totals.FeeRows != 8 {
		t.Fatalf("source counts = %d/%d, want 4/8", totals.SettledRows, totals.FeeRows)
	}
	if totals.EstimatedSalesAmount != 37.5 || totals.EstimatedSalesMatched != 1 || totals.EstimatedSalesTotal != 1 {
		t.Fatalf("estimated sales = amount:%v matched:%d total:%d, want 37.5/1/1", totals.EstimatedSalesAmount, totals.EstimatedSalesMatched, totals.EstimatedSalesTotal)
	}

	empty, err := store.profitDailySummary(ctx, "UTC", "day", "panda-buy-empty-shop")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Totals.Units != 0 || empty.Totals.RefundTotal != 0 || empty.Totals.PaybackAmount != 0 {
		t.Fatalf("empty shop totals should be zero, got %+v", empty.Totals)
	}
}
