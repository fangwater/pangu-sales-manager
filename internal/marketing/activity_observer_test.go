package marketing

import (
	"testing"
	"time"

	"pangu-sales-manager/internal/temu"
)

func TestActivityObserverCarriesSKCStateAndProducesSKUPrices(t *testing.T) {
	observer := NewActivityObserver()
	first := observer.Observe(observerSnapshot(time.Date(2026, 8, 29, 17, 0, 0, 0, time.UTC), 90))
	if first.States[10].Status != SKCActivityConfirmed || first.States[10].ActiveEnrollID != 1 || len(first.SKUPrices) != 1 || first.SKUPrices[0].ResolvedPrice != 700 {
		t.Fatalf("unexpected initial observation: %#v", first)
	}
	second := observer.Observe(observerSnapshot(time.Date(2026, 8, 29, 17, 1, 0, 0, time.UTC), 90))
	stock := second.Enrollments[EnrollmentObservationKey{EnrollID: 1, SKCID: 10}]
	if stock.IntervalConsumedStock != 0 || !second.States[10].CarriedForward || second.SKUPrices[0].ResolvedPrice != 700 {
		t.Fatalf("unchanged observation was not carried: %#v", second)
	}
	third := observer.Observe(observerSnapshot(time.Date(2026, 8, 29, 17, 2, 0, 0, time.UTC), 88))
	if third.Enrollments[EnrollmentObservationKey{EnrollID: 1, SKCID: 10}].IntervalConsumedStock != 2 || third.States[10].Reason != "interval_unique_consumption" {
		t.Fatalf("minute consumption was not observed: %#v", third)
	}
}

func observerSnapshot(capturedAt time.Time, remaining int64) Snapshot {
	return Snapshot{StartedAt: capturedAt.Add(-time.Second), CompletedAt: capturedAt, Enrollments: []temu.MarketingEnrollment{{
		EnrollID: 1, ActivityStock: 100, RemainingActivityStock: remaining,
		AssignedSessions: []temu.MarketingEnrollmentSession{{SessionID: 11, SessionStatus: 2, SiteID: 100}},
		SKCList: []temu.MarketingEnrollmentSKC{{SKCID: 10, Currency: "USD", SKUList: []temu.MarketingEnrollmentSKU{{
			SKUID: 101, SitePriceList: []temu.MarketingEnrollmentPrice{{SiteID: 100, DailyPrice: 1000, ActivityPrice: 700}},
		}}}},
	}}}
}
