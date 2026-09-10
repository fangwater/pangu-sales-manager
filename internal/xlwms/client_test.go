package xlwms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newFakeServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewClient(server.URL)
}

func writeEnvelope(t *testing.T, writer http.ResponseWriter, success bool, data any, errMessage string) {
	t.Helper()
	payload := map[string]any{"success": success}
	if success {
		payload["data"] = data
	} else {
		payload["error"] = errMessage
	}
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		t.Fatal(err)
	}
}

func TestListMappingsUnionsAccountsAndSkipsBundles(t *testing.T) {
	requestsByAccount := map[string]int{}
	client := newFakeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/platform-orders/accounts":
			writeEnvelope(t, writer, true, []Account{{Key: "arp", Available: true}, {Key: "dps", Available: true}}, "")
		case "/product-pairings":
			account := request.URL.Query().Get("account")
			requestsByAccount[account]++
			switch account {
			case "arp":
				writeEnvelope(t, writer, true, pairingPageData{
					Pages: 1,
					Records: []Pairing{
						{PlatformSKU: "a", Items: []PairingItem{{SystemSKU: "b", Quantity: 1}}},
						{PlatformSKU: "bundle", Items: []PairingItem{{SystemSKU: "x", Quantity: 1}, {SystemSKU: "y", Quantity: 1}}},
					},
				}, "")
			case "dps":
				writeEnvelope(t, writer, true, pairingPageData{
					Pages: 1,
					Records: []Pairing{
						{PlatformSKU: "c", Items: []PairingItem{{SystemSKU: "b", Quantity: 2}}},
					},
				}, "")
			default:
				t.Fatalf("unexpected account %q", account)
			}
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	})

	mappings, skipped, err := client.ListMappings(context.Background(), "634418225219052")
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 {
		t.Fatalf("skipped bundles = %d, want 1", skipped)
	}
	if len(mappings) != 2 {
		t.Fatalf("mappings = %+v, want 2 entries", mappings)
	}
	if mappings["a"].WarehouseSKU != "b" || mappings["a"].Quantity != 1 {
		t.Fatalf("mapping a = %+v", mappings["a"])
	}
	if mappings["c"].WarehouseSKU != "b" || mappings["c"].Quantity != 2 {
		t.Fatalf("mapping c = %+v", mappings["c"])
	}
	if requestsByAccount["arp"] != 1 || requestsByAccount["dps"] != 1 {
		t.Fatalf("expected exactly one page request per account, got %+v", requestsByAccount)
	}
}

func TestCreateMappingSucceedsIfAnyAccountAccepts(t *testing.T) {
	var attempted []string
	client := newFakeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/platform-orders/accounts":
			writeEnvelope(t, writer, true, []Account{{Key: "arp"}, {Key: "dps"}}, "")
		case "/product-pairings":
			var payload struct {
				Account     string        `json:"account"`
				StoreCode   string        `json:"store_code"`
				PlatformSKU string        `json:"platform_sku"`
				Items       []PairingItem `json:"items"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			attempted = append(attempted, payload.Account)
			if payload.StoreCode != "634418225219052" || payload.PlatformSKU != "sku-a" || len(payload.Items) != 1 || payload.Items[0].SystemSKU != "warehouse-a" || payload.Items[0].Quantity != 3 {
				t.Fatalf("unexpected payload %+v", payload)
			}
			if payload.Account == "arp" {
				writeEnvelope(t, writer, false, nil, "store not authorized for this account")
				return
			}
			writeEnvelope(t, writer, true, map[string]any{}, "")
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	})

	if err := client.CreateMapping(context.Background(), "634418225219052", "sku-a", "warehouse-a", 3); err != nil {
		t.Fatalf("CreateMapping returned error: %v", err)
	}
	if len(attempted) != 2 || attempted[0] != "arp" || attempted[1] != "dps" {
		t.Fatalf("expected to try arp then dps, got %+v", attempted)
	}
}

func TestCreateMappingFailsIfAllAccountsReject(t *testing.T) {
	client := newFakeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/platform-orders/accounts":
			writeEnvelope(t, writer, true, []Account{{Key: "arp"}, {Key: "dps"}}, "")
		case "/product-pairings":
			writeEnvelope(t, writer, false, nil, "store not authorized for this account")
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	})

	if err := client.CreateMapping(context.Background(), "634418225219052", "sku-a", "warehouse-a", 1); err == nil {
		t.Fatal("expected error when every account rejects the write")
	}
}
