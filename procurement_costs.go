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

// Finance reads the calculation owned by restock; it cannot mutate cost inputs.
func (s *APIServer) fifoCosts(w http.ResponseWriter, r *http.Request) {
	endpoint := strings.TrimRight(envOr("RESTOCK_API_BASE_URL", "http://127.0.0.1:18110"), "/") + "/api/costs/fifo?" + r.URL.Query().Encode()
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		s.internalError(w, "create FIFO request", err)
		return
	}
	response, err := (&http.Client{Timeout: 45 * time.Second}).Do(request)
	if err != nil {
		writeJSON(w, 503, apiResponse{Success: false, Error: "FIFO 成本核算暂不可用"})
		return
	}
	defer response.Body.Close()
	var payload apiResponse
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(&payload) != nil || !payload.Success {
		writeJSON(w, 503, apiResponse{Success: false, Error: "FIFO 成本核算暂不可用，请在备货系统查看数据源状态"})
		return
	}
	writeJSON(w, 200, payload)
}
