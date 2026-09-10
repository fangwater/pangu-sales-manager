package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pangu-sales-manager/internal/xlwms"
)

func TestApplyXLWMSMappingsOverwritesManualMapping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()

	const shopKey = "xlwms-sync-test"
	const platformSKU = "XLWMS-TEST-PLATFORM-SKU"
	const oldWarehouseSKU = "XLWMS-TEST-OLD-WSKU"
	const newWarehouseSKU = "XLWMS-TEST-NEW-WSKU"
	cleanup := func() {
		store.db.ExecContext(ctx, `DELETE FROM sku_mappings WHERE shop_key=$1`, shopKey)
		store.db.ExecContext(ctx, `DELETE FROM canonical_skus WHERE warehouse_sku IN ($1,$2)`, oldWarehouseSKU, newWarehouseSKU)
	}
	cleanup()
	defer cleanup()

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO canonical_skus (warehouse_sku) VALUES ($1)
	`, oldWarehouseSKU); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO sku_mappings (platform, shop_key, platform_sku, warehouse_sku, conversion_factor, mapping_source, mapping_status)
		VALUES ('temu', $1, $2, $3, 1, 'operator', 'manual')
	`, shopKey, platformSKU, oldWarehouseSKU); err != nil {
		t.Fatal(err)
	}

	upserted, err := store.applyXLWMSMappings(ctx, shopKey, map[string]xlwms.Mapping{
		platformSKU: {PlatformSKU: platformSKU, WarehouseSKU: newWarehouseSKU, Quantity: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if upserted != 1 {
		t.Fatalf("upserted = %d, want 1", upserted)
	}

	var warehouseSKU, mappingSource, mappingStatus string
	var factor float64
	if err := store.db.QueryRowContext(ctx, `
		SELECT warehouse_sku, conversion_factor, mapping_source, mapping_status
		FROM sku_mappings WHERE platform='temu' AND shop_key=$1 AND platform_sku=$2
	`, shopKey, platformSKU).Scan(&warehouseSKU, &factor, &mappingSource, &mappingStatus); err != nil {
		t.Fatal(err)
	}
	if warehouseSKU != newWarehouseSKU || factor != 2 || mappingSource != "xlwms" || mappingStatus != "mapped" {
		t.Fatalf("got warehouse_sku=%s factor=%v source=%s status=%s, want %s/2/xlwms/mapped",
			warehouseSKU, factor, mappingSource, mappingStatus, newWarehouseSKU)
	}
}

func TestUpdateMappingWriteThroughToXLWMS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()

	const shopKey = "panda-homes"
	const platformSKU = "XLWMS-WRITE-THROUGH-TEST-SKU"
	const warehouseSKU = "XLWMS-WRITE-THROUGH-TEST-WSKU"
	cleanup := func() {
		store.db.ExecContext(ctx, `DELETE FROM sku_mappings WHERE shop_key=$1 AND platform_sku=$2`, shopKey, platformSKU)
		store.db.ExecContext(ctx, `DELETE FROM canonical_skus WHERE warehouse_sku=$1`, warehouseSKU)
	}
	cleanup()
	defer cleanup()

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO canonical_skus (warehouse_sku) VALUES ($1)
	`, warehouseSKU); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO sku_mappings (platform, shop_key, platform_sku, warehouse_sku, conversion_factor, mapping_source, mapping_status)
		VALUES ('temu', $1, $2, $3, 1, 'source', 'inferred')
	`, shopKey, platformSKU, warehouseSKU); err != nil {
		t.Fatal(err)
	}

	var createCalls []string
	fakeXLWMS := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/platform-orders/accounts":
			json.NewEncoder(writer).Encode(map[string]any{"success": true, "data": []xlwms.Account{{Key: "arp"}, {Key: "dps"}}})
		case "/product-pairings":
			var payload struct {
				Account string `json:"account"`
			}
			json.NewDecoder(request.Body).Decode(&payload)
			createCalls = append(createCalls, payload.Account)
			if payload.Account == "arp" {
				json.NewEncoder(writer).Encode(map[string]any{"success": false, "error": "not authorized"})
				return
			}
			json.NewEncoder(writer).Encode(map[string]any{"success": true, "data": map[string]any{}})
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer fakeXLWMS.Close()

	server := &APIServer{
		store:  store,
		syncer: &Syncer{xlwmsClient: xlwms.NewClient(fakeXLWMS.URL)},
		logger: slog.Default(),
	}

	body := `{"warehouse_sku":"` + warehouseSKU + `","conversion_factor":3}`
	request := httptest.NewRequest(http.MethodPatch, "/api/mappings/temu/"+shopKey+"/"+platformSKU, strings.NewReader(body))
	request.SetPathValue("platform", "temu")
	request.SetPathValue("shop", shopKey)
	request.SetPathValue("sku", platformSKU)
	recorder := httptest.NewRecorder()

	server.updateMapping(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(createCalls) != 2 || createCalls[0] != "arp" || createCalls[1] != "dps" {
		t.Fatalf("expected write-through to try arp then dps, got %+v", createCalls)
	}

	var gotWarehouseSKU string
	var gotFactor float64
	if err := store.db.QueryRowContext(ctx, `
		SELECT warehouse_sku, conversion_factor FROM sku_mappings
		WHERE platform='temu' AND shop_key=$1 AND platform_sku=$2
	`, shopKey, platformSKU).Scan(&gotWarehouseSKU, &gotFactor); err != nil {
		t.Fatal(err)
	}
	if gotWarehouseSKU != warehouseSKU || gotFactor != 3 {
		t.Fatalf("local cache = %s/%v, want %s/3", gotWarehouseSKU, gotFactor, warehouseSKU)
	}
}

func TestUpdateMappingRejectsNonIntegerFactorForTemu(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()

	server := &APIServer{
		store:  store,
		syncer: &Syncer{xlwmsClient: xlwms.NewClient("http://unused.invalid")},
		logger: slog.Default(),
	}

	request := httptest.NewRequest(http.MethodPatch, "/api/mappings/temu/panda-homes/some-sku", strings.NewReader(`{"warehouse_sku":"w","conversion_factor":1.5}`))
	request.SetPathValue("platform", "temu")
	request.SetPathValue("shop", "panda-homes")
	request.SetPathValue("sku", "some-sku")
	recorder := httptest.NewRecorder()

	server.updateMapping(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s, want 400", recorder.Code, recorder.Body.String())
	}
}
