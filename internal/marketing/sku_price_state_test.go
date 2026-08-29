package marketing

import (
	"testing"
	"time"
)

func TestResolveSKUPriceStateUsesConfirmedActivity(t *testing.T) {
	now := time.Now()
	state := ResolveSKUPriceState(now, SKCActivityState{Status: SKCActivityConfirmed, ActiveEnrollID: 1}, []SKUActivityPricePoint{
		{EnrollID: 1, SKCID: 10, SKUID: 101, SiteID: 100, Currency: "USD", DailyPrice: 1000, ActivityPrice: 700},
		{EnrollID: 2, SKCID: 10, SKUID: 101, SiteID: 100, Currency: "USD", DailyPrice: 1000, ActivityPrice: 800},
	})
	if state.Status != SKCActivityConfirmed || state.ResolvedPrice != 700 || state.PriceSource != SKUPriceSourceActivity || state.ActiveEnrollID != 1 {
		t.Fatalf("unexpected confirmed price: %#v", state)
	}
}

func TestResolveSKUPriceStateFallsBackToUniqueDailyPriceOnWarning(t *testing.T) {
	state := ResolveSKUPriceState(time.Now(), SKCActivityState{Status: SKCActivityWarning}, []SKUActivityPricePoint{
		{EnrollID: 1, SKCID: 10, SKUID: 101, SiteID: 100, DailyPrice: 1000, ActivityPrice: 700},
		{EnrollID: 2, SKCID: 10, SKUID: 101, SiteID: 100, DailyPrice: 1000, ActivityPrice: 800},
	})
	if state.Status != SKCActivityWarning || state.ResolvedPrice != 1000 || state.PriceSource != SKUPriceSourceDaily {
		t.Fatalf("unexpected warning fallback: %#v", state)
	}
}

func TestResolveSKUPriceStateWarnsOnDailyPriceConflict(t *testing.T) {
	state := ResolveSKUPriceState(time.Now(), SKCActivityState{Status: SKCActivityWarning}, []SKUActivityPricePoint{
		{EnrollID: 1, SKCID: 10, SKUID: 101, SiteID: 100, DailyPrice: 1000},
		{EnrollID: 2, SKCID: 10, SKUID: 101, SiteID: 100, DailyPrice: 1100},
	})
	if state.Status != SKCActivityWarning || state.ResolvedPrice != 0 || state.PriceSource != SKUPriceSourceUnresolved || state.Reason != "skc_state_warning_daily_price_conflict" {
		t.Fatalf("daily price conflict was not preserved: %#v", state)
	}
}
