package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestTemuSalesActualDateAndCancellation(t *testing.T) {
	for _, tt := range []struct {
		status   int
		eligible bool
		name     string
		qty      float64
	}{
		{1, true, "pending_processing", 2}, {2, true, "pending_shipping", 2}, {3, false, "cancelled", 0}, {4, true, "shipped", 2}, {5, true, "delivered", 2}, {6, false, "status_6", 0},
	} {
		raw := fmt.Sprintf(`{"parentOrderMap":{"parentOrderStatus":%d,"parentOrderTime":1791730000},"orderList":[{"orderSn":"line","quantity":2,"orderStatus":%d,"productList":[{"extCode":"sale-100","productSkuId":123}]}]}`, tt.status, tt.status)
		o, lines, err := parseTemuSalesOrder(SourceOrder{Platform: "temu", ShopKey: "test", OrderNo: "po", FirstSeenAt: time.Unix(1791800000, 0), RawPayload: json.RawMessage(raw)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if o.OccurredAt.Unix() != 1791730000 || o.OccurredAtSource != "platform_order_time" || o.NormalizedStatus != tt.name || o.SalesEligible != tt.eligible || len(lines) != 1 || lines[0].Quantity != tt.qty || lines[0].PlatformSKU != "sale-100" {
			t.Fatalf("status %d: %+v %+v", tt.status, o, lines)
		}
	}
	o, lines, err := parseTemuSalesOrder(SourceOrder{FirstSeenAt: time.Unix(100, 0), RawPayload: json.RawMessage(`{"parentOrderMap":{"parentOrderStatus":4},"orderList":[{"orderSn":"a","orderStatus":3,"quantity":2,"extCode":"sku"},{"orderSn":"b","orderStatus":4,"quantity":3,"extCode":"sku"}]}`)}, nil)
	if err != nil || o.OccurredAtSource != "first_seen" || lines[0].Quantity != 0 || lines[1].Quantity != 3 {
		t.Fatalf("partial cancel: %+v %v", lines, err)
	}
}

func TestTemuFullSnapshotRemovesStaleLinesAndPreservesMapping(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	store, err := openStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	suffix := fmt.Sprint(time.Now().UnixNano())
	sku := "sales-sync-" + suffix
	o := SourceOrder{Platform: "temu", ShopKey: "sales-test", OrderNo: suffix, OccurredAt: time.Now(), OccurredAtSource: "platform_order_time", SalesEligible: true, RawPayload: json.RawMessage(`{}`)}
	defer store.db.Exec(`DELETE FROM normalized_orders WHERE source_order_no=$1`, suffix)
	line := SourceLine{Platform: o.Platform, ShopKey: o.ShopKey, OrderNo: o.OrderNo, SourceLineKey: "a", PlatformSKU: sku, SuggestedSKU: sku, Quantity: 3, ConversionFactor: 2, MappingSource: "manual", MappingStatus: "manual"}
	removed := line
	removed.SourceLineKey = "b"
	if _, _, err = store.upsertSourceBatch(ctx, []SourceOrder{o}, []SourceLine{line, removed}); err != nil {
		t.Fatal(err)
	}
	line.Quantity = 4
	line.ConversionFactor = 1
	line.MappingSource = "platform_ext_code"
	line.MappingStatus = "identity"
	for i := 0; i < 2; i++ {
		if _, _, err = store.upsertSourceBatch(ctx, []SourceOrder{o}, []SourceLine{line}); err != nil {
			t.Fatal(err)
		}
	}
	var qty, warehouse float64
	var n int
	if err = store.db.QueryRow(`SELECT sum(quantity),sum(warehouse_quantity),count(*) FROM normalized_order_lines l JOIN normalized_orders o ON o.id=l.order_id WHERE o.source_order_no=$1`, suffix).Scan(&qty, &warehouse, &n); err != nil {
		t.Fatal(err)
	}
	if qty != 4 || warehouse != 8 || n != 2 {
		t.Fatalf("snapshot/idempotency: qty=%v warehouse=%v lines=%d", qty, warehouse, n)
	}
}
