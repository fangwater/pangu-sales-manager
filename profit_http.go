package main

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

var profitImporting atomic.Bool

func (s *APIServer) profitSummary(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	tables, err := s.store.profitTableCounts(ctx)
	if err != nil {
		s.internalError(writer, "load temu profit summary", err)
		return
	}
	latest, err := s.store.latestProfitImport(ctx)
	if err != nil {
		s.internalError(writer, "load temu profit import status", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: ProfitSummary{
		Tables: tables, LatestImport: latest, Importing: profitImporting.Load(),
	}})
}

func (s *APIServer) profitDailySummary(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	period := strings.TrimSpace(request.URL.Query().Get("period"))
	shopKey := strings.TrimSpace(request.URL.Query().Get("shop_key"))
	data, err := s.store.profitDailySummary(ctx, s.timezone, period, shopKey)
	if err != nil {
		s.internalError(writer, "load temu profit daily summary", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: data})
}

func (s *APIServer) profitSKUSummary(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	location, err := time.LoadLocation(s.timezone)
	if err != nil {
		location = time.FixedZone("CST", 8*60*60)
	}
	now := time.Now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)

	end := today.AddDate(0, 0, 1)
	if raw := strings.TrimSpace(request.URL.Query().Get("end")); raw != "" {
		parsed, err := time.ParseInLocation("2006-01-02", raw, location)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "end 参数格式应为 YYYY-MM-DD"})
			return
		}
		end = parsed.AddDate(0, 0, 1)
	}
	start := end.AddDate(0, 0, -30)
	if raw := strings.TrimSpace(request.URL.Query().Get("start")); raw != "" {
		parsed, err := time.ParseInLocation("2006-01-02", raw, location)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "start 参数格式应为 YYYY-MM-DD"})
			return
		}
		start = parsed
	}
	if !start.Before(end) {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "start 必须早于 end"})
		return
	}

	shopKey := strings.TrimSpace(request.URL.Query().Get("shop_key"))
	data, err := s.store.profitSKUSummary(ctx, s.timezone, start, end, shopKey)
	if err != nil {
		s.internalError(writer, "load temu profit sku summary", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: data})
}

func (s *APIServer) profitUnsettledSummary(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	shopKey := strings.TrimSpace(request.URL.Query().Get("shop_key"))
	data, err := s.store.profitUnsettledSummary(ctx, shopKey)
	if err != nil {
		s.internalError(writer, "load temu profit unsettled summary", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: data})
}

func (s *APIServer) importProfit(writer http.ResponseWriter, request *http.Request) {
	if !profitImporting.CompareAndSwap(false, true) {
		writeJSON(writer, http.StatusConflict, apiResponse{Success: false, Error: "利润表正在导入"})
		return
	}
	defer profitImporting.Store(false)

	request.Body = http.MaxBytesReader(writer, request.Body, profitUploadMaxBytes)
	if err := request.ParseMultipartForm(profitUploadMaxBytes); err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "上传文件不能超过 32MB"})
		return
	}
	shopKey := strings.TrimSpace(request.FormValue("shop_key"))
	if shopKey == "" {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "shop_key 不能为空"})
		return
	}
	hint := strings.TrimSpace(request.FormValue("table"))
	file, header, err := request.FormFile("file")
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "请上传 xlsx 或 zip"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: "读取上传文件失败"})
		return
	}

	filename := filepath.Base(header.Filename)
	location, err := time.LoadLocation(s.timezone)
	if err != nil {
		location = time.FixedZone("CST", 8*60*60)
	}
	sourceKind, batches, err := parseProfitUpload(filename, data, profitParseOptions{
		ShopKey: shopKey, Hint: hint, Location: location,
	})
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), 4*time.Minute)
	defer cancel()
	runID, err := s.store.beginProfitImport(ctx, shopKey, sourceKind, filename)
	if err != nil {
		s.internalError(writer, "start temu profit import", err)
		return
	}
	result, applyErr := s.store.applyProfitImport(ctx, shopKey, batches)
	result.SourceKind = sourceKind
	result.SourceName = filename
	status := "succeeded"
	if applyErr != nil {
		status = "failed"
	}
	if finishErr := s.store.finishProfitImport(ctx, runID, status, result, applyErr); finishErr != nil {
		s.logger.Error("finish temu profit import", "error", finishErr)
	}
	if applyErr != nil {
		writeJSON(writer, http.StatusBadRequest, apiResponse{Success: false, Error: applyErr.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: result})
}
