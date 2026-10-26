package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProcurementCostsReadRestockWithoutRoundingOrInventingMissingCosts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/costs/export" || r.Method != http.MethodGet {
			t.Errorf("unexpected restock request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"version":1,"owner":"pangu-restock-manager","tickets":[{"purchase_currency":"","effective_date":null,"lines":[{"amount":"0.3000000003"}],"costs":[]}]}}`))
	}))
	defer upstream.Close()
	t.Setenv("RESTOCK_API_BASE_URL", upstream.URL)
	w := httptest.NewRecorder()
	(&APIServer{}).procurementCosts(w, httptest.NewRequest(http.MethodGet, "/api/profit/costs", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"0.3000000003"`) || !strings.Contains(w.Body.String(), `"effective_date":null`) {
		t.Fatalf("unexpected cost snapshot: %d %s", w.Code, w.Body.String())
	}
}

func TestProcurementCostsUpstreamFailuresAreNotEmptySuccessfulCosts(t *testing.T) {
	for _, body := range []string{`{"success":false}`, `{"success":true,"data":{"version":2,"owner":"other"}}`, `not-json`} {
		t.Run(body, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer upstream.Close()
			t.Setenv("RESTOCK_API_BASE_URL", upstream.URL)
			w := httptest.NewRecorder()
			(&APIServer{}).procurementCosts(w, httptest.NewRequest(http.MethodGet, "/api/profit/costs", nil))
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("failed costs must be unavailable, got %d", w.Code)
			}
		})
	}
}
