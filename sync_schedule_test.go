package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestDecideSyncMode(t *testing.T) {
	now := time.Date(2026, 9, 14, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		orders    int64
		last      sql.NullTime
		wantMode  string
		wantRetry time.Duration
	}{
		{name: "first run", orders: 0, wantMode: "full"},
		{name: "recent failed full with data", orders: 10, last: validTime(now.Add(-time.Minute)), wantMode: "incremental"},
		{name: "daily full due", orders: 10, last: validTime(now.Add(-fullSyncInterval)), wantMode: "full"},
		{name: "empty store backs off", orders: 0, last: validTime(now.Add(-5 * time.Minute)), wantRetry: 25 * time.Minute},
		{name: "empty store retries later", orders: 0, last: validTime(now.Add(-emptyStoreFullRetryDelay)), wantMode: "full"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, retry := decideSyncMode(test.orders, test.last, now)
			if mode != test.wantMode || retry != test.wantRetry {
				t.Fatalf("decideSyncMode() = (%q, %s), want (%q, %s)", mode, retry, test.wantMode, test.wantRetry)
			}
		})
	}
}

func TestSyncRetryDelay(t *testing.T) {
	interval := time.Minute
	for failures, want := range map[int]time.Duration{
		0: interval,
		1: 2 * time.Minute,
		2: 4 * time.Minute,
		5: 30 * time.Minute,
		9: 30 * time.Minute,
	} {
		if got := syncRetryDelay(interval, failures); got != want {
			t.Errorf("syncRetryDelay(%s, %d) = %s, want %s", interval, failures, got, want)
		}
	}
}

func TestSheinLineQueryDoesNotRequireRemovedMappingTable(t *testing.T) {
	if strings.Contains(sheinLineQuery, "shein_sku_mappings") {
		t.Fatal("SHEIN line sync must not depend on the removed remote mapping table")
	}
	if !strings.Contains(sheinLineQuery, "seller_sku_fallback") {
		t.Fatal("SHEIN line sync must retain the seller SKU fallback")
	}
}

func TestUpsertMappingDoesNotDowngradeMappedValue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	mapped := SourceLine{
		Platform: "shein", ShopKey: "mapping-priority-test", PlatformSKU: "PLATFORM-SKU",
		SuggestedSKU: "MAPPED-SKU", ConversionFactor: 2, MappingSource: "source", MappingStatus: "mapped",
	}
	if _, _, err := upsertMapping(ctx, tx, mapped); err != nil {
		t.Fatal(err)
	}
	inferred := mapped
	inferred.SuggestedSKU = "INFERRED-SKU"
	inferred.ConversionFactor = 1
	inferred.MappingSource = "seller_sku_fallback"
	inferred.MappingStatus = "inferred"
	warehouseSKU, factor, err := upsertMapping(ctx, tx, inferred)
	if err != nil {
		t.Fatal(err)
	}
	if warehouseSKU != "MAPPED-SKU" || factor != 2 {
		t.Fatalf("mapping downgraded to (%q, %v)", warehouseSKU, factor)
	}
}

func validTime(value time.Time) sql.NullTime {
	return sql.NullTime{Time: value, Valid: true}
}
