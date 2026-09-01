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
