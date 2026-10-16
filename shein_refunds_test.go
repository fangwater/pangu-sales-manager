package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Run the production aggregation against synthetic data in an isolated database.
func sheinRefundFixtureLines(t *testing.T, store *Store, orderNo, payload string) []SourceLine {
	t.Helper()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TEMP TABLE shein_refund_fixture(order_no text, detail_payload jsonb, last_seen_at timestamptz) ON COMMIT DROP`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO shein_refund_fixture VALUES($1,$2,now())`, orderNo, payload); err != nil {
		t.Fatal(err)
	}
	query := strings.ReplaceAll(sheinLineQuery, "shein_beauty_hangers_home.shein_orders", "shein_refund_fixture")
	rows, err := tx.QueryContext(ctx, query, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []SourceLine
	for rows.Next() {
		line := SourceLine{Platform: "shein", ShopKey: "refund-test-" + orderNo}
		var warehouse string
		var price sql.NullFloat64
		var raw []byte
		if err = rows.Scan(&line.OrderNo, &line.PlatformSKU, &line.SuggestedSKU,
			&line.ProductName, &line.VariantName, &warehouse, &line.Quantity,
			&line.ConversionFactor, &line.MappingSource, &line.MappingStatus,
			&price, &line.Currency, &raw); err != nil {
			t.Fatal(err)
		}
		line.SourceLineKey = line.PlatformSKU
		line.RawPayload = raw
		lines = append(lines, line)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func isolatedSheinRefundStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated TEST_DATABASE_URL required")
	}
	store, err := openStore(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSheinRefundStatusAndOriginalItems(t *testing.T) {
	store := isolatedSheinRefundStore(t)
	for _, tt := range []struct {
		name, status string
		want         float64
	}{
		{"delivered", `"newGoodsStatus":5,"goodsStatus":2`, 1},
		{"processing", `"newGoodsStatus":1,"goodsStatus":1`, 1},
		{"shipped", `"newGoodsStatus":4,"goodsStatus":1`, 1},
		{"pickup", `"newGoodsStatus":7,"goodsStatus":1`, 1},
		{"refunded", `"newGoodsStatus":6,"goodsStatus":3`, 0},
		{"string refund", `"newGoodsStatus":"6","goodsStatus":"3"`, 0},
		{"legacy refund", `"goodsStatus":3`, 0},
		{"empty new status", `"newGoodsStatus":" ","goodsStatus":3`, 0},
		{"null new status", `"newGoodsStatus":null,"goodsStatus":3`, 0},
		{"new status wins", `"newGoodsStatus":5,"goodsStatus":3`, 1},
		{"unknown state retained", `"newGoodsStatus":99,"goodsStatus":1`, 1},
		{"missing state retained", `"goodsId":123`, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := `{"orderGoodsInfoList":[{"skuCode":"sku","sellerSku":"warehouse",` + tt.status + `}]}`
			lines := sheinRefundFixtureLines(t, store, "status", payload)
			if len(lines) != 1 || lines[0].Quantity != tt.want {
				t.Fatalf("lines=%+v, want one line with quantity=%v", lines, tt.want)
			}
			var raw struct {
				Items    []json.RawMessage `json:"items"`
				Original int               `json:"original_quantity"`
				Refunded int               `json:"refunded_quantity"`
			}
			if err := json.Unmarshal(lines[0].RawPayload, &raw); err != nil {
				t.Fatal(err)
			}
			if len(raw.Items) != 1 || raw.Original != 1 || float64(raw.Refunded) != 1-tt.want {
				t.Fatalf("raw items/counts not preserved: %+v", raw)
			}
		})
	}
}

func TestSheinPartialRefundResyncAndRestock(t *testing.T) {
	store := isolatedSheinRefundStore(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	shop := "refund-test-" + suffix
	sku := "shein-refund-" + suffix
	order := SourceOrder{Platform: "shein", ShopKey: shop, OrderNo: suffix,
		SourceStatus: "5", NormalizedStatus: "delivered", SalesEligible: true,
		OccurredAt: time.Now(), OccurredAtSource: "platform_order_time"}
	t.Cleanup(func() {
		store.db.Exec(`DELETE FROM normalized_orders WHERE platform='shein' AND shop_key=$1`, shop)
		store.db.Exec(`DELETE FROM sku_mappings WHERE platform='shein' AND shop_key=$1`, shop)
		store.db.Exec(`DELETE FROM canonical_skus WHERE warehouse_sku IN ($1,$2)`, sku, sku+"-other")
	})
	// An existing delivered order has two units of one SKU and a different SKU.
	payload := fmt.Sprintf(`{"orderGoodsInfoList":[
	 {"goodsId":1,"skuCode":%q,"sellerSku":%q,"newGoodsStatus":5},
	 {"goodsId":2,"skuCode":%q,"sellerSku":%q,"newGoodsStatus":5},
	 {"goodsId":3,"skuCode":%q,"sellerSku":%q,"newGoodsStatus":5}]}`,
		sku, sku, sku, sku, sku+"-other", sku+"-other")
	save := func(payload string) {
		t.Helper()
		lines := sheinRefundFixtureLines(t, store, suffix, payload)
		for i := range lines {
			lines[i].ConversionFactor = 2
			lines[i].MappingSource, lines[i].MappingStatus = "operator", "manual"
		}
		order.RawPayload = json.RawMessage(payload)
		if _, _, err := store.upsertSourceBatch(ctx, []SourceOrder{order}, lines); err != nil {
			t.Fatal(err)
		}
	}
	save(payload)
	// One of the duplicate-SKU units and the other SKU are refunded, while the
	// parent remains delivered. Repeating the sync must not subtract twice.
	refunded := strings.Replace(payload, fmt.Sprintf(`"goodsId":2,"skuCode":%q,"sellerSku":%q,"newGoodsStatus":5`, sku, sku), fmt.Sprintf(`"goodsId":2,"skuCode":%q,"sellerSku":%q,"newGoodsStatus":6`, sku, sku), 1)
	refunded = strings.Replace(refunded, fmt.Sprintf(`"goodsId":3,"skuCode":%q,"sellerSku":%q,"newGoodsStatus":5`, sku+"-other", sku+"-other"), fmt.Sprintf(`"goodsId":3,"skuCode":%q,"sellerSku":%q,"newGoodsStatus":6`, sku+"-other", sku+"-other"), 1)
	for i := 0; i < 2; i++ {
		save(refunded)
	}
	var qty, warehouseQty, original, refund float64
	var lineCount int
	err := store.db.QueryRow(`SELECT SUM(l.quantity),SUM(l.warehouse_quantity),COUNT(*),
	 SUM((l.raw_payload->>'original_quantity')::numeric),SUM((l.raw_payload->>'refunded_quantity')::numeric)
	 FROM normalized_order_lines l JOIN normalized_orders o ON o.id=l.order_id WHERE o.shop_key=$1`, shop).Scan(&qty, &warehouseQty, &lineCount, &original, &refund)
	if err != nil {
		t.Fatal(err)
	}
	if qty != 1 || warehouseQty != 2 || lineCount != 2 || original != 3 || refund != 2 {
		t.Fatalf("qty=%v warehouse=%v lines=%d original=%v refunded=%v", qty, warehouseQty, lineCount, original, refund)
	}
	for _, version := range []string{"2", "3"} {
		recorder := httptest.NewRecorder()
		server := &APIServer{store: store, timezone: "Asia/Shanghai"}
		server.restockSnapshot(recorder, httptest.NewRequest("GET", "/api/restock/snapshot?version="+version, nil))
		if recorder.Code != 200 {
			t.Fatalf("restock v%s: %d %s", version, recorder.Code, recorder.Body.String())
		}
		var response struct {
			Data struct {
				Points []struct {
					SKU            string  `json:"sku"`
					Units          float64 `json:"units"`
					WarehouseUnits float64 `json:"warehouse_units"`
				} `json:"points"`
			} `json:"data"`
		}
		if err = json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		var total, warehouse float64
		for _, p := range response.Data.Points {
			if p.SKU == sku || p.SKU == sku+"-other" {
				total += p.Units
				warehouse += p.WarehouseUnits
			}
		}
		if total != 1 || warehouse != 2 {
			t.Fatalf("restock v%s units=%v warehouse=%v", version, total, warehouse)
		}
	}
	// The next snapshot can restore a status; quantities are current state,
	// never a permanent or cumulative decrement.
	save(payload)
	if err = store.db.QueryRow(`SELECT SUM(l.quantity) FROM normalized_order_lines l JOIN normalized_orders o ON o.id=l.order_id WHERE o.shop_key=$1`, shop).Scan(&qty); err != nil || qty != 3 {
		t.Fatalf("restored qty=%v err=%v", qty, err)
	}
}
