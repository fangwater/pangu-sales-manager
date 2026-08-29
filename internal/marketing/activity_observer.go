package marketing

import (
	"sort"
	"sync"
	"time"
)

type EnrollmentObservationKey struct {
	EnrollID int64
	SKCID    int64
}

type EnrollmentStockObservation struct {
	EnrollID                       int64     `json:"enroll_id"`
	SKCID                          int64     `json:"skc_id"`
	ActivityStock                  int64     `json:"activity_stock"`
	RemainingActivityStock         int64     `json:"remaining_activity_stock"`
	PreviousRemainingActivityStock *int64    `json:"previous_remaining_activity_stock,omitempty"`
	IntervalConsumedStock          int64     `json:"interval_consumed_stock"`
	IntervalIncreasedStock         int64     `json:"interval_increased_stock"`
	CumulativeConsumedStock        int64     `json:"cumulative_consumed_stock"`
	CapturedAt                     time.Time `json:"captured_at"`
}

type ActivityObservation struct {
	CapturedAt  time.Time
	Enrollments map[EnrollmentObservationKey]EnrollmentStockObservation
	States      map[int64]SKCActivityState
	SKUPrices   []SKUPriceState
}

type ActivityObserver struct {
	mu                sync.RWMutex
	previousRemaining map[EnrollmentObservationKey]int64
	states            map[int64]SKCActivityState
	latest            ActivityObservation
}

func NewActivityObserver() *ActivityObserver {
	return &ActivityObserver{
		previousRemaining: make(map[EnrollmentObservationKey]int64),
		states:            make(map[int64]SKCActivityState),
	}
}

func (o *ActivityObserver) Observe(snapshot Snapshot) ActivityObservation {
	o.mu.Lock()
	defer o.mu.Unlock()

	observation := ActivityObservation{
		CapturedAt:  snapshot.CompletedAt,
		Enrollments: make(map[EnrollmentObservationKey]EnrollmentStockObservation),
		States:      make(map[int64]SKCActivityState),
	}
	pointsBySKC := make(map[int64][]ActivityStockPoint)
	pricePointsBySKU := make(map[int64][]SKUActivityPricePoint)
	currentRemaining := make(map[EnrollmentObservationKey]int64)

	for _, enrollment := range snapshot.Enrollments {
		for _, skc := range enrollment.SKCList {
			key := EnrollmentObservationKey{EnrollID: enrollment.EnrollID, SKCID: skc.SKCID}
			stock := EnrollmentStockObservation{
				EnrollID: enrollment.EnrollID, SKCID: skc.SKCID,
				ActivityStock: enrollment.ActivityStock, RemainingActivityStock: enrollment.RemainingActivityStock,
				CumulativeConsumedStock: max(enrollment.ActivityStock-enrollment.RemainingActivityStock, 0),
				CapturedAt:              snapshot.CompletedAt,
			}
			if previous, ok := o.previousRemaining[key]; ok {
				stock.PreviousRemainingActivityStock = observerInt64Pointer(previous)
				if previous > stock.RemainingActivityStock {
					stock.IntervalConsumedStock = previous - stock.RemainingActivityStock
				} else if previous < stock.RemainingActivityStock {
					stock.IntervalIncreasedStock = stock.RemainingActivityStock - previous
				}
			}
			observation.Enrollments[key] = stock
			currentRemaining[key] = stock.RemainingActivityStock
			pointsBySKC[skc.SKCID] = append(pointsBySKC[skc.SKCID], ActivityStockPoint{
				EnrollID: enrollment.EnrollID, SKCID: skc.SKCID,
				CumulativeConsumed: stock.CumulativeConsumedStock, IntervalConsumed: stock.IntervalConsumedStock,
			})
			for _, sku := range skc.SKUList {
				for _, price := range sku.SitePriceList {
					pricePointsBySKU[sku.SKUID] = append(pricePointsBySKU[sku.SKUID], SKUActivityPricePoint{
						EnrollID: enrollment.EnrollID, SKCID: skc.SKCID, SKUID: sku.SKUID,
						SiteID: price.SiteID, Currency: observerFirstNonEmpty(sku.Currency, skc.Currency, enrollment.Currency),
						DailyPrice: price.DailyPrice, ActivityPrice: price.ActivityPrice,
					})
				}
			}
		}
	}

	stateSKCs := make(map[int64]struct{}, len(pointsBySKC)+len(o.states))
	for skcID := range pointsBySKC {
		stateSKCs[skcID] = struct{}{}
	}
	for skcID, previous := range o.states {
		if previous.Reason != "no_current_activity" {
			stateSKCs[skcID] = struct{}{}
		}
	}
	for skcID := range stateSKCs {
		var previous *SKCActivityState
		if value, ok := o.states[skcID]; ok {
			copy := value
			previous = &copy
		}
		observation.States[skcID] = ResolveSKCActivityState(snapshot.CompletedAt, skcID, pointsBySKC[skcID], previous)
	}

	skuIDs := make([]int64, 0, len(pricePointsBySKU))
	for skuID := range pricePointsBySKU {
		skuIDs = append(skuIDs, skuID)
	}
	sort.Slice(skuIDs, func(left, right int) bool { return skuIDs[left] < skuIDs[right] })
	for _, skuID := range skuIDs {
		candidates := pricePointsBySKU[skuID]
		skcIDs := make([]int64, 0, len(candidates))
		for _, candidate := range candidates {
			skcIDs = append(skcIDs, candidate.SKCID)
		}
		skcIDs = uniqueSorted(skcIDs)
		skcState := observation.States[candidates[0].SKCID]
		if len(skcIDs) > 1 {
			skcState = SKCActivityState{Status: SKCActivityWarning, Reason: "sku_multiple_skc"}
		}
		price := ResolveSKUPriceState(snapshot.CompletedAt, skcState, candidates)
		if len(skcIDs) > 1 {
			price.Status = SKCActivityWarning
			price.ActiveEnrollID = 0
			price.Reason = "sku_multiple_skc"
		}
		observation.SKUPrices = append(observation.SKUPrices, price)
	}

	o.previousRemaining = currentRemaining
	o.states = cloneSKCStates(observation.States)
	o.latest = cloneActivityObservation(observation)
	return cloneActivityObservation(observation)
}

func (o *ActivityObserver) Latest() ActivityObservation {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return cloneActivityObservation(o.latest)
}

func cloneActivityObservation(source ActivityObservation) ActivityObservation {
	copy := ActivityObservation{CapturedAt: source.CapturedAt, Enrollments: make(map[EnrollmentObservationKey]EnrollmentStockObservation, len(source.Enrollments)), States: cloneSKCStates(source.States)}
	for key, value := range source.Enrollments {
		if value.PreviousRemainingActivityStock != nil {
			value.PreviousRemainingActivityStock = observerInt64Pointer(*value.PreviousRemainingActivityStock)
		}
		copy.Enrollments[key] = value
	}
	copy.SKUPrices = append([]SKUPriceState(nil), source.SKUPrices...)
	for index := range copy.SKUPrices {
		copy.SKUPrices[index].CandidateEnrollIDs = append([]int64(nil), copy.SKUPrices[index].CandidateEnrollIDs...)
	}
	return copy
}

func cloneSKCStates(source map[int64]SKCActivityState) map[int64]SKCActivityState {
	copy := make(map[int64]SKCActivityState, len(source))
	for key, value := range source {
		value.CandidateEnrollIDs = append([]int64(nil), value.CandidateEnrollIDs...)
		value.EvidenceEnrollIDs = append([]int64(nil), value.EvidenceEnrollIDs...)
		copy[key] = value
	}
	return copy
}

func observerFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func observerInt64Pointer(value int64) *int64 {
	copy := value
	return &copy
}
