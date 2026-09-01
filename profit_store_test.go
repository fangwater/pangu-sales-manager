package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestProfitImportOverwritesByBusinessKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()

	first := []ProfitImportBatch{{
		File: "发货面单费-已出账.xlsx",
		Kind: ProfitFileShippingPosted,
		Shipping: []TemuProfitShippingLabelFee{{
			ShopKey: "panda-homes", BillingPhase: ProfitBillingPosted,
			PackageNo: "PK-1", TrackingNo: "TRK-1", BillType: "支出", Remark: "发货面单费（全段）",
			FreightAmount: -5, Currency: "USD", ReconcileStatus: "支出成功",
		}},
	}}
	result, err := store.applyProfitImport(ctx, "panda-homes", first)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsUpserted != 1 || result.Tables[0].Inserted != 1 {
		t.Fatalf("first import: %+v", result)
	}

	occurred := time.Date(2026, 8, 30, 4, 20, 11, 0, time.FixedZone("CST", 8*60*60))
	second := []ProfitImportBatch{{
		File: "发货面单费-已出账.xlsx",
		Kind: ProfitFileShippingPosted,
		Shipping: []TemuProfitShippingLabelFee{{
			ShopKey: "panda-homes", BillingPhase: ProfitBillingPosted,
			PackageNo: "PK-1", TrackingNo: "TRK-1", BillType: "支出", Remark: "发货面单费（全段）",
			FreightAmount: -6.5, Currency: "USD", ReconcileStatus: "支出成功", OccurredAt: &occurred,
		}},
	}}
	result, err = store.applyProfitImport(ctx, "panda-homes", second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Tables[0].Updated != 1 || result.Tables[0].Inserted != 0 {
		t.Fatalf("second import should overwrite: %+v", result)
	}

	var amount float64
	var count int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*), MAX(freight_amount)
		FROM temu_profit_shipping_label_fees
		WHERE shop_key='panda-homes' AND package_no='PK-1'
	`).Scan(&count, &amount); err != nil {
		t.Fatal(err)
	}
	if count != 1 || amount != -6.5 {
		t.Fatalf("count=%d amount=%v", count, amount)
	}
}

func openTestProfitStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		url = "host=/var/run/postgresql dbname=pangu_sales user=fanghaizhou sslmode=disable"
	}
	store, err := openStore(ctx, url)
	if err != nil {
		t.Skipf("local postgres unavailable: %v", err)
		return nil
	}
	if _, err := store.db.ExecContext(ctx, `
		DELETE FROM temu_profit_shipping_label_fees
		WHERE shop_key='panda-homes' AND package_no='PK-1'
	`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store
}
