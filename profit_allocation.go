package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

type profitAllocationPolicy struct {
	Method     string    `json:"method"`
	CostMethod string    `json:"cost_method"`
	Version    int       `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (s *APIServer) profitAllocationPolicy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var policy profitAllocationPolicy
	err := s.store.db.QueryRowContext(ctx, `SELECT method,cost_method,version,updated_at FROM profit_allocation_policy WHERE id=1`).Scan(&policy.Method, &policy.CostMethod, &policy.Version, &policy.UpdatedAt)
	if err != nil {
		s.internalError(w, "load allocation policy", err)
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: policy})
}
func (s *APIServer) saveProfitAllocationPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Pangu-Request") != "finance" {
		writeJSON(w, 403, apiResponse{Success: false, Error: "请求来源无效"})
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			writeJSON(w, 403, apiResponse{Success: false, Error: "请求来源无效"})
			return
		}
	}
	var input struct {
		Method  string `json:"method"`
		Version int    `json:"version"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, 400, apiResponse{Success: false, Error: "规则数据无效"})
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || input.Version < 1 || !map[string]bool{"declared_value": true, "quantity": true, "manual": true}[input.Method] {
		writeJSON(w, 400, apiResponse{Success: false, Error: "分摊规则或版本无效"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		s.internalError(w, "begin allocation policy", err)
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE profit_allocation_policy SET method=$1,version=version+1,updated_at=now() WHERE id=1 AND version=$2`, input.Method, input.Version)
	if err != nil {
		s.internalError(w, "save allocation policy", err)
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		s.internalError(w, "save allocation policy count", err)
		return
	}
	if count != 1 {
		writeJSON(w, 409, apiResponse{Success: false, Error: "规则已变更，请刷新后重试"})
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO profit_allocation_policy_history(version,method,cost_method) SELECT version,method,cost_method FROM profit_allocation_policy WHERE id=1`); err != nil {
		s.internalError(w, "audit allocation policy", err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.internalError(w, "commit allocation policy", err)
		return
	}
	s.profitAllocationPolicy(w, r)
}
