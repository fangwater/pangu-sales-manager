package main

import (
	"context"
	"testing"
	"time"

	"pangu-sales-manager/internal/marketing"
	"pangu-sales-manager/internal/temu"
)

func TestResolveSKUPriceQueriesReadsCurrentPriceFromMemory(t *testing.T) {
	observer := marketing.NewActivityObserver()
	now := time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC)
	observer.Observe(marketing.Snapshot{StartedAt: now.Add(-time.Second), CompletedAt: now, Enrollments: []temu.MarketingEnrollment{{
		EnrollID: 1, ActivityStock: 100, RemainingActivityStock: 90,
		AssignedSessions: []temu.MarketingEnrollmentSession{{SessionID: 11, SessionStatus: 2, SiteID: 100}},
		SKCList: []temu.MarketingEnrollmentSKC{{SKCID: 10, Currency: "USD", SKUList: []temu.MarketingEnrollmentSKU{{
			SKUID: 101, SitePriceList: []temu.MarketingEnrollmentPrice{{SiteID: 100, DailyPrice: 1000, ActivityPrice: 700}},
		}}}},
	}}})

	results, err := resolveSKUPriceQueries(context.Background(), observer, nil, []SKUPriceQueryItem{{SKUID: 101}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != marketing.SKCActivityConfirmed || results[0].Price != 700 || results[0].MatchMethod != "current_memory" || results[0].ObservedAt == nil || !results[0].ObservedAt.Equal(now) {
		t.Fatalf("unexpected current price result: %#v", results)
	}
}

func TestResolveSKUPriceQueriesReportsMissingCurrentSKU(t *testing.T) {
	results, err := resolveSKUPriceQueries(context.Background(), marketing.NewActivityObserver(), nil, []SKUPriceQueryItem{{SKUID: 999}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != marketing.SKCActivityWarning || results[0].Reason != "current_sku_not_found" {
		t.Fatalf("unexpected missing SKU result: %#v", results)
	}
}
