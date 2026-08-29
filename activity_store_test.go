package main

import (
	"testing"

	"pangu-sales-manager/internal/marketing"
)

func TestSameSKUPriceIntervalDetectsOnlyMaterialChanges(t *testing.T) {
	existing := marketing.SKUPriceInterval{
		SKUID: 101, SKCID: 10, Status: marketing.SKCActivityConfirmed,
		ActiveEnrollID: 1, CandidateEnrollIDs: []int64{1, 2}, Currency: "USD",
		DailyPrice: 1000, ActivityPrice: 700, Price: 700,
		PriceSource: marketing.SKUPriceSourceActivity,
	}
	state := marketing.SKUPriceState{
		SKUID: 101, SKCID: 10, Status: marketing.SKCActivityConfirmed,
		ActiveEnrollID: 1, CandidateEnrollIDs: []int64{1, 2}, Currency: "USD",
		DailyPrice: 1000, ActivityPrice: 700, ResolvedPrice: 700,
		PriceSource: marketing.SKUPriceSourceActivity,
	}
	if !sameSKUPriceInterval(existing, state) {
		t.Fatal("unchanged price state was treated as a delta")
	}
	state.ResolvedPrice = 699
	if sameSKUPriceInterval(existing, state) {
		t.Fatal("price change was not detected")
	}
}
