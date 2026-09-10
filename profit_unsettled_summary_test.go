package main

import (
	"context"
	"testing"
	"time"
)

func TestProfitUnsettledSummaryRollsUpByShopAndSKU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()

	const shopKey = "profit-unsettled-test"
	cleanup := func() {
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_unsettle_orders WHERE shop_key=$1`, shopKey)
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_shipping_label_fees WHERE shop_key=$1`, shopKey)
	}
	cleanup()
	defer cleanup()

	var orderID int64
	if err := store.db.QueryRowContext(ctx, `
		INSERT INTO temu_profit_unsettle_orders
			(shop_key, po_no, sales_receipt, sales_receipt_after_discount, sales_chargeback, freight_receipt, freight_receipt_after_discount, freight_chargeback)
		VALUES ($1, 'PU-PO-1', 100, 90, -5, 20, 18, -1)
		RETURNING id
	`, shopKey).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO temu_profit_unsettle_skus (unsettle_order_id, line_no, sku_id, sku_name, quantity, declared_price)
		VALUES ($1, 1, 555, 'Pending Product', 3, 7)
	`, orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO temu_profit_shipping_label_fees (shop_key, billing_phase, package_no, tracking_no, bill_type, freight_amount)
		VALUES ($1, 'pending', 'PU-PKG-1', 'PU-TRK-1', '支出', -6)
	`, shopKey); err != nil {
		t.Fatal(err)
	}

	result, err := store.profitUnsettledSummary(ctx, shopKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Shops) != 1 {
		t.Fatalf("shops = %+v, want 1", result.Shops)
	}
	shop := result.Shops[0]
	if shop.SalesReceipt != 100 || shop.SalesChargeback != -5 || shop.FreightReceipt != 20 || shop.FreightChargeback != -1 {
		t.Fatalf("shop summary = %+v, unexpected values", shop)
	}
	if shop.PendingShippingLabelFee != -6 {
		t.Fatalf("pending shipping label fee = %v, want -6", shop.PendingShippingLabelFee)
	}
	if len(result.SKUs) != 1 {
		t.Fatalf("skus = %+v, want 1", result.SKUs)
	}
	sku := result.SKUs[0]
	if sku.SKUID != 555 || sku.SKUName != "Pending Product" || sku.Quantity != 3 || sku.DeclaredTotal != 21 {
		t.Fatalf("sku row = %+v, want id 555 quantity 3 declared_total 21", sku)
	}
}
