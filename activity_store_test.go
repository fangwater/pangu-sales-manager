package main

import (
	"testing"
	"time"

	"pangu-sales-manager/internal/marketing"
	"pangu-sales-manager/internal/temu"
)

func TestFlattenActivitySnapshotCalculatesMinuteDeltaAndSKUPrices(t *testing.T) {
	snapshot := marketing.Snapshot{
		StartedAt:   time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC),
		CompletedAt: time.Date(2026, 8, 29, 16, 0, 2, 0, time.UTC),
		Enrollments: []temu.MarketingEnrollment{{
			EnrollID: 1, ActivityType: 13, ActivityTypeName: "限时活动",
			ActivityStock: 100, RemainingActivityStock: 85,
			AssignedSessions: []temu.MarketingEnrollmentSession{{SessionID: 11, SessionStatus: 2, SiteID: 100}},
			SKCList: []temu.MarketingEnrollmentSKC{{SKCID: 10, Currency: "USD", SKUList: []temu.MarketingEnrollmentSKU{{
				SKUID: 101, SitePriceList: []temu.MarketingEnrollmentPrice{{SiteID: 100, DailyPrice: 900, ActivityPrice: 700}},
			}}}},
		}},
	}
	previous := map[activityEnrollmentKey]int64{{EnrollID: 1, SKCID: 10}: 90}

	enrollments, prices := flattenActivitySnapshot(snapshot, previous)
	if len(enrollments) != 1 {
		t.Fatalf("enrollment records = %#v", enrollments)
	}
	record := enrollments[0]
	if record.PreviousRemaining == nil || *record.PreviousRemaining != 90 || record.IntervalConsumed != 5 || record.IntervalIncreased != 0 || record.CumulativeConsumed != 15 {
		t.Fatalf("unexpected stock delta: %#v", record)
	}
	if len(prices) != 1 || prices[0].EnrollID != 1 || prices[0].SKUID != 101 || prices[0].SessionID != 11 || prices[0].DailyPrice != 900 || prices[0].ActivityPrice != 700 {
		t.Fatalf("unexpected SKU price points: %#v", prices)
	}
}

func TestFlattenActivitySnapshotDetectsStockIncrease(t *testing.T) {
	snapshot := marketing.Snapshot{Enrollments: []temu.MarketingEnrollment{{
		EnrollID: 1, ActivityStock: 100, RemainingActivityStock: 95,
		SKCList: []temu.MarketingEnrollmentSKC{{SKCID: 10}},
	}}}
	previous := map[activityEnrollmentKey]int64{{EnrollID: 1, SKCID: 10}: 90}

	enrollments, _ := flattenActivitySnapshot(snapshot, previous)
	if len(enrollments) != 1 || enrollments[0].IntervalConsumed != 0 || enrollments[0].IntervalIncreased != 5 {
		t.Fatalf("stock increase was not detected: %#v", enrollments)
	}
}

func TestResolveSKUPriceSnapshotsProducesOneResolvedRowPerSKU(t *testing.T) {
	records := []skuPriceSnapshotRecord{
		{EnrollID: 1, SKCID: 10, SKUID: 101, SiteID: 100, Currency: "USD", DailyPrice: 1000, ActivityPrice: 700},
		{EnrollID: 2, SKCID: 10, SKUID: 101, SiteID: 100, Currency: "USD", DailyPrice: 1000, ActivityPrice: 800},
	}
	states := map[int64]marketing.SKCActivityState{10: {SKCID: 10, Status: marketing.SKCActivityConfirmed, ActiveEnrollID: 1}}

	prices := resolveSKUPriceSnapshots(time.Now(), records, states)
	if len(prices) != 1 || prices[0].Status != marketing.SKCActivityConfirmed || prices[0].ResolvedPrice != 700 || prices[0].PriceSource != marketing.SKUPriceSourceActivity {
		t.Fatalf("unexpected resolved SKU prices: %#v", prices)
	}
}

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
