package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Restock owns the editable cost ledger. Finance consumes its current snapshot;
// amounts without a currency or effective date remain incomplete cost inputs.
func (s *APIServer) procurementCosts(w http.ResponseWriter, r *http.Request) {
	endpoint := strings.TrimRight(envOr("RESTOCK_API_BASE_URL", "http://127.0.0.1:18110"), "/") + "/api/costs/export"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		s.internalError(w, "create restock cost request", err)
		return
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "备货成本数据暂不可用"})
		return
	}
	defer response.Body.Close()
	var payload struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&payload) != nil || !payload.Success {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "备货成本数据暂不可用"})
		return
	}
	var identity struct {
		Version int    `json:"version"`
		Owner   string `json:"owner"`
	}
	if json.Unmarshal(payload.Data, &identity) != nil || identity.Version != 1 || identity.Owner != "pangu-restock-manager" {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Success: false, Error: "备货成本数据格式无效"})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: payload.Data})
}
