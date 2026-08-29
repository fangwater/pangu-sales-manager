package marketing

import "time"

const (
	SKUPriceSourceActivity   = "activity"
	SKUPriceSourceDaily      = "daily"
	SKUPriceSourceUnresolved = "unresolved"
)

type SKUActivityPricePoint struct {
	EnrollID      int64
	SKCID         int64
	SKUID         int64
	SiteID        int64
	Currency      string
	DailyPrice    int64
	ActivityPrice int64
}

type SKUPriceState struct {
	SKUID              int64     `json:"sku_id"`
	SKCID              int64     `json:"skc_id"`
	SiteID             int64     `json:"site_id"`
	Status             string    `json:"status"`
	ActiveEnrollID     int64     `json:"active_enroll_id,omitempty"`
	CandidateEnrollIDs []int64   `json:"candidate_enroll_ids"`
	Currency           string    `json:"currency"`
	DailyPrice         int64     `json:"daily_price"`
	ActivityPrice      int64     `json:"activity_price"`
	ResolvedPrice      int64     `json:"resolved_price"`
	PriceSource        string    `json:"price_source"`
	Reason             string    `json:"reason"`
	CapturedAt         time.Time `json:"captured_at"`
}

func ResolveSKUPriceState(now time.Time, skcState SKCActivityState, candidates []SKUActivityPricePoint) SKUPriceState {
	state := SKUPriceState{CapturedAt: now, Status: SKCActivityWarning, PriceSource: SKUPriceSourceUnresolved}
	if len(candidates) == 0 {
		state.Reason = "no_price_candidates"
		return state
	}
	state.SKUID = candidates[0].SKUID
	state.SKCID = candidates[0].SKCID
	state.SiteID = candidates[0].SiteID
	state.Currency = firstPriceCurrency(candidates)
	state.CandidateEnrollIDs = priceCandidateEnrollIDs(candidates)
	dailyPrices := uniquePositivePrices(candidates, false)
	if len(dailyPrices) == 1 {
		state.DailyPrice = dailyPrices[0]
	}

	if skcState.Status == SKCActivityConfirmed && skcState.ActiveEnrollID > 0 {
		state.ActiveEnrollID = skcState.ActiveEnrollID
		selected := make([]SKUActivityPricePoint, 0)
		for _, candidate := range candidates {
			if candidate.EnrollID == skcState.ActiveEnrollID {
				selected = append(selected, candidate)
			}
		}
		activityPrices := uniquePositivePrices(selected, true)
		if len(activityPrices) == 1 {
			state.Status = SKCActivityConfirmed
			state.ActivityPrice = activityPrices[0]
			state.ResolvedPrice = activityPrices[0]
			state.PriceSource = SKUPriceSourceActivity
			state.Reason = "confirmed_activity_price"
			if state.DailyPrice == 0 {
				selectedDaily := uniquePositivePrices(selected, false)
				if len(selectedDaily) == 1 {
					state.DailyPrice = selectedDaily[0]
				}
			}
			return state
		}
		if len(activityPrices) > 1 {
			state.Reason = "confirmed_activity_price_conflict"
		} else {
			state.Reason = "confirmed_activity_price_missing"
		}
	} else {
		state.Reason = "skc_state_warning"
	}

	if len(dailyPrices) == 1 {
		state.ResolvedPrice = dailyPrices[0]
		state.PriceSource = SKUPriceSourceDaily
		state.Reason += "_daily_fallback"
		return state
	}
	if len(dailyPrices) > 1 {
		state.Reason += "_daily_price_conflict"
	} else {
		state.Reason += "_daily_price_missing"
	}
	return state
}

func uniquePositivePrices(candidates []SKUActivityPricePoint, activity bool) []int64 {
	prices := make([]int64, 0)
	for _, candidate := range candidates {
		value := candidate.DailyPrice
		if activity {
			value = candidate.ActivityPrice
		}
		if value > 0 {
			prices = append(prices, value)
		}
	}
	return uniqueSorted(prices)
}

func priceCandidateEnrollIDs(candidates []SKUActivityPricePoint) []int64 {
	values := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		values = append(values, candidate.EnrollID)
	}
	return uniqueSorted(values)
}

func firstPriceCurrency(candidates []SKUActivityPricePoint) string {
	for _, candidate := range candidates {
		if candidate.Currency != "" {
			return candidate.Currency
		}
	}
	return ""
}
