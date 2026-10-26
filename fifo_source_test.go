package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFIFODemandSnapshotKeepsAllShopsAndNoBuyerData(t *testing.T) {
	store, ctx, _, _ := attributionFixture(t)
	_, err := store.db.ExecContext(ctx, `UPDATE normalized_orders SET warehouse_code='W',raw_payload=raw_payload||'{"buyerEmail":"private@example.test"}'::jsonb WHERE shop_key LIKE 'attribution-test%';
 UPDATE normalized_order_lines SET conversion_factor=2,warehouse_quantity=quantity*2 WHERE order_id IN(SELECT id FROM normalized_orders WHERE shop_key LIKE 'attribution-test%')`)
	if err != nil {
		t.Fatal(err)
	}
	server, err := newAPIServer(store, &Syncer{}, nil, nil, "Asia/Shanghai", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/restock/cost-demands?start=2026-09-01&end=2026-09-30&shop=attribution-test&warehouse=OTHER", nil)
	response := httptest.NewRecorder()
	server.fifoDemands(response, request)
	if response.Code != 200 {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Demands []map[string]any `json:"demands"`
			SHA     string           `json:"sha256"`
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	other := false
	for _, d := range envelope.Data.Demands {
		if d["shop_key"] == "attribution-test-other" {
			other = true
		}
		if strings.HasPrefix(d["shop_key"].(string), "attribution-test") && (d["warehouse_quantity"] != "2.0000" || d["warehouse_code"] != "W") {
			t.Fatalf("wrong quantities: %v", d)
		}
	}
	if !other || len(envelope.Data.SHA) != 64 || strings.Contains(response.Body.String(), "private@example") || strings.Contains(response.Body.String(), "raw_payload") {
		t.Fatal("missing shared demand or leaked buyer payload")
	}
	again := httptest.NewRecorder()
	server.fifoDemands(again, request)
	var second struct {
		Data struct {
			SHA string `json:"sha256"`
		}
	}
	json.Unmarshal(again.Body.Bytes(), &second)
	if envelope.Data.SHA != second.Data.SHA {
		t.Fatal("unstable source fingerprint")
	}
	for _, query := range []string{"", "?start=wrong", "?start=2020-01-01&end=2026-09-30"} {
		w := httptest.NewRecorder()
		server.fifoDemands(w, httptest.NewRequest("GET", "/api/restock/cost-demands"+query, nil))
		if w.Code != 400 {
			t.Fatalf("accepted invalid range: %s", query)
		}
	}
}
