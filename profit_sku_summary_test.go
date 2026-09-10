package main

import (
	"context"
	"testing"
	"time"
)

func TestProfitSKUSummaryRollsUpByPlatformSKU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()

	const shopKey = "profit-sku-summary-test"
	const platformSKU = "PSS-PLATFORM-SKU"
	const warehouseSKU = "PSS-WAREHOUSE-SKU"
	cleanup := func() {
		store.db.ExecContext(ctx, `DELETE FROM normalized_orders WHERE shop_key=$1`, shopKey)
		store.db.ExecContext(ctx, `DELETE FROM canonical_skus WHERE warehouse_sku=$1`, warehouseSKU)
		store.db.ExecContext(ctx, `DELETE FROM temu_profit_settled_flows WHERE shop_key=$1`, shopKey)
	}
	cleanup()
	defer cleanup()

	now := time.Now().UTC()

	if _, err := store.db.ExecContext(ctx, `INSERT INTO canonical_skus (warehouse_sku) VALUES ($1)`, warehouseSKU); err != nil {
		t.Fatal(err)
	}
	var soldOrderID int64
	if err := store.db.QueryRowContext(ctx, `
		INSERT INTO normalized_orders (platform, shop_key, source_order_no, occurred_at, occurred_at_source, sales_eligible)
		VALUES ('temu', $1, 'PSS-SOLD-1', $2, 'test', true) RETURNING id
	`, shopKey, now).Scan(&soldOrderID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO normalized_order_lines (order_id, source_line_key, platform_sku, warehouse_sku, quantity, warehouse_quantity, unit_price)
		VALUES ($1, 'L1', $2, $3, 4, 4, 10)
	`, soldOrderID, platformSKU, warehouseSKU); err != nil {
		t.Fatal(err)
	}
	var cancelledOrderID int64
	if err := store.db.QueryRowContext(ctx, `
		INSERT INTO normalized_orders (platform, shop_key, source_order_no, occurred_at, occurred_at_source, sales_eligible)
		VALUES ('temu', $1, 'PSS-CANCELLED-1', $2, 'test', false) RETURNING id
	`, shopKey, now).Scan(&cancelledOrderID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO normalized_order_lines (order_id, source_line_key, platform_sku, warehouse_sku, quantity, warehouse_quantity)
		VALUES ($1, 'L1', $2, $3, 1, 1)
	`, cancelledOrderID, platformSKU, warehouseSKU); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO temu_profit_settled_flows (shop_key, flow_id, trade_type, settle_amount, sku_ext_code, sku_name, received_at)
		VALUES ($1, 'PSS-FLOW-1', '销售回款', 80, $2, 'Test Product', $3),
		       ($1, 'PSS-FLOW-2', '销售冲回', -8, $2, 'Test Product', $3)
	`, shopKey, platformSKU, now); err != nil {
		t.Fatal(err)
	}

	result, err := store.profitSKUSummary(ctx, "UTC", now.AddDate(0, 0, -1), now.AddDate(0, 0, 1), shopKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %+v, want 1 row for %s", result.Rows, platformSKU)
	}
	row := result.Rows[0]
	if row.PlatformSKU != platformSKU {
		t.Fatalf("platform_sku = %s, want %s", row.PlatformSKU, platformSKU)
	}
	if row.Units != 4 {
		t.Fatalf("units = %v, want 4", row.Units)
	}
	if row.RefundOrders != 1 {
		t.Fatalf("refund orders = %v, want 1", row.RefundOrders)
	}
	if row.EstimatedSalesAmount != 40 || row.EstimatedSalesMatched != 1 || row.EstimatedSalesTotal != 1 {
		t.Fatalf("estimated sales = %+v, want amount 40 matched 1 total 1", row)
	}
	if row.SalesChargebacks != 1 {
		t.Fatalf("sales chargebacks = %v, want 1", row.SalesChargebacks)
	}
	if row.SalesReceiptAmount != 80 || row.SalesChargebackAmount != -8 || row.PaybackAmount != 72 {
		t.Fatalf("settled amounts = %+v, want receipt 80 chargeback -8 payback 72", row)
	}
	if row.SKUName != "Test Product" {
		t.Fatalf("sku name = %q, want %q", row.SKUName, "Test Product")
	}
}
