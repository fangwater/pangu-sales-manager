package marketing

import (
	"testing"
	"time"

	"pangu-sales-manager/internal/temu"
)

func TestActivityRowsExposeEnrollmentStockSessionsAndEveryPriceLayer(t *testing.T) {
	syncer := NewSyncer(nil)
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	syncer.publish(Snapshot{
		StartedAt: now.Add(-time.Second), CompletedAt: now,
		EnrollmentPages: 1,
		Enrollments: []temu.MarketingEnrollment{{
			EnrollID: 1, ProductID: 2, ActivityType: 13, ActivityTypeName: "活动",
			ActivityStock: 100, RemainingActivityStock: 90, EnrollStatus: 4,
			AssignedSessions: []temu.MarketingEnrollmentSession{
				{SessionID: 11, SessionStatus: 2, SiteID: 100, SiteName: "美国站"},
				{SessionID: 12, SessionStatus: 3, SiteID: 200, SiteName: "加拿大站"},
			},
			SKCList: []temu.MarketingEnrollmentSKC{{
				SKCID: 10, Currency: "USD",
				SKUList: []temu.MarketingEnrollmentSKU{
					{SKUID: 101, SitePriceList: []temu.MarketingEnrollmentPrice{{SiteID: 100, SiteName: "美国站", DailyPrice: 850, ActivityPrice: 650}}},
					{SKUID: 102, SitePriceList: []temu.MarketingEnrollmentPrice{{SiteID: 200, SiteName: "加拿大站", DailyPrice: 900, ActivityPrice: 700}}},
				},
			}},
		}},
	})

	rows, summary, snapshot, err := syncer.ActivityRows(ActivityRowFilter{SKUID: 101})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Successful() || summary.EnrollmentCount != 1 || summary.ActiveEnrollmentCount != 1 || summary.StockConsumedEnrollmentCount != 1 {
		t.Fatalf("unexpected snapshot summary: %#v", summary)
	}
	if summary.TotalActivityStock != 100 || summary.TotalRemainingActivityStock != 90 || len(rows) != 2 {
		t.Fatalf("unexpected rows or stock summary: rows=%#v summary=%#v", rows, summary)
	}
	current := rows[0]
	if current.SiteID != 100 || current.SessionID != 11 || current.EnrollmentSKUCount != 2 || current.ConsumedActivityStock != 10 {
		t.Fatalf("activity/session/stock fields were not preserved: %#v", current)
	}
	if current.SiteDailyPrice != 850 || current.SiteActivityPrice != 650 {
		t.Fatalf("site price was not preserved: %#v", current)
	}
	filtered, _, _, err := syncer.ActivityRows(ActivityRowFilter{SKUID: 101, SiteID: 200})
	if err != nil || len(filtered) != 1 || filtered[0].SessionID != 12 || filtered[0].SiteActivityPrice != 0 {
		t.Fatalf("site filter result = %#v, %v", filtered, err)
	}
}
