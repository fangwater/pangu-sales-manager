package marketing

import (
	"errors"
	"testing"
	"time"

	"pangu-sales-manager/internal/temu"
)

func TestEffectivePricesUsesLowestCurrentPricePerSKUAndSite(t *testing.T) {
	syncer := NewSyncer(nil)
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	syncer.publish(Snapshot{
		StartedAt: now.Add(-time.Second), CompletedAt: now,
		DetailsByType: map[int64]temu.MarketingActivityDetail{}, DetailErrors: map[int64]string{},
		GoodsBySKC: map[int64]temu.GoodsSummary{10: {
			ProductSKCID: 10, SKCSiteStatus: temu.SKCSiteStatusOnShelf,
			ProductSKUSummaries: []temu.GoodsSKUInfo{{ProductSKUID: 101}},
		}},
		Enrollments: []temu.MarketingEnrollment{
			testEnrollment(1, 10, 101, 100, 700, 900, 2),
			testEnrollment(2, 10, 101, 100, 600, 900, 2),
			testEnrollment(3, 10, 101, 100, 300, 900, 3),
		},
	})

	items, snapshot, err := syncer.EffectivePrices(EffectivePriceFilter{SKCID: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Successful() || len(items) != 1 || items[0].EffectiveActivityPrice != 600 {
		t.Fatalf("unexpected effective price result: %#v", items)
	}
	if len(items[0].ActiveActivities) != 2 || len(items[0].WinningActivities) != 1 {
		t.Fatalf("unexpected activity candidates: %#v", items[0])
	}
}

func TestEffectivePricesRequiresSuccessfulSnapshot(t *testing.T) {
	items, _, err := NewSyncer(nil).EffectivePrices(EffectivePriceFilter{})
	if !errors.Is(err, ErrSnapshotUnavailable) || items != nil {
		t.Fatalf("unavailable snapshot result = %#v, %v", items, err)
	}
}

func testEnrollment(id, skcID, skuID, siteID, activityPrice, dailyPrice int64, status int) temu.MarketingEnrollment {
	return temu.MarketingEnrollment{
		EnrollID: id, ProductID: 1, ActivityType: 13, ActivityTypeName: "活动",
		AssignedSessions: []temu.MarketingEnrollmentSession{{SessionID: id, SessionStatus: status, SiteID: siteID}},
		SKCList: []temu.MarketingEnrollmentSKC{{SKCID: skcID, Currency: "USD", SKUList: []temu.MarketingEnrollmentSKU{{
			SKUID: skuID, SitePriceList: []temu.MarketingEnrollmentPrice{{SiteID: siteID, DailyPrice: dailyPrice, ActivityPrice: activityPrice}},
		}}}},
	}
}
