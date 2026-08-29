package marketing

import (
	"testing"
	"time"
)

func TestResolveSKCActivityStateInitialUniqueConsumption(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	state := ResolveSKCActivityState(now, 10, []ActivityStockPoint{
		{EnrollID: 1, SKCID: 10, CumulativeConsumed: 488},
		{EnrollID: 2, SKCID: 10, CumulativeConsumed: 0},
	}, nil)
	if state.Status != SKCActivityConfirmed || state.ActiveEnrollID != 1 || state.Reason != "initial_unique_cumulative_consumption" {
		t.Fatalf("unexpected initial state: %#v", state)
	}
}

func TestResolveSKCActivityStateInitialConflict(t *testing.T) {
	state := ResolveSKCActivityState(time.Now(), 10, []ActivityStockPoint{
		{EnrollID: 1, CumulativeConsumed: 2}, {EnrollID: 2, CumulativeConsumed: 3},
	}, nil)
	if state.Status != SKCActivityConflict || len(state.EvidenceEnrollIDs) != 2 {
		t.Fatalf("unexpected conflict state: %#v", state)
	}
}

func TestResolveSKCActivityStateCarriesConfirmedStateWithoutConsumption(t *testing.T) {
	started := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	previous := SKCActivityState{SKCID: 10, Status: SKCActivityConfirmed, ActiveEnrollID: 1, CandidateEnrollIDs: []int64{1, 2}, EvidenceEnrollIDs: []int64{1}, StateStartedAt: started, LastEvidenceAt: timePointer(started)}
	state := ResolveSKCActivityState(started.Add(time.Minute), 10, []ActivityStockPoint{{EnrollID: 1}, {EnrollID: 2}}, &previous)
	if state.Status != SKCActivityConfirmed || state.ActiveEnrollID != 1 || !state.CarriedForward || !state.StateStartedAt.Equal(started) || len(state.EvidenceEnrollIDs) != 1 || state.EvidenceEnrollIDs[0] != 1 {
		t.Fatalf("confirmed state was not carried: %#v", state)
	}
}

func TestResolveSKCActivityStateSwitchesOnUniqueIntervalConsumption(t *testing.T) {
	started := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	previous := SKCActivityState{SKCID: 10, Status: SKCActivityConfirmed, ActiveEnrollID: 1, CandidateEnrollIDs: []int64{1, 2}, StateStartedAt: started}
	now := started.Add(time.Minute)
	state := ResolveSKCActivityState(now, 10, []ActivityStockPoint{{EnrollID: 1}, {EnrollID: 2, IntervalConsumed: 1}}, &previous)
	if state.Status != SKCActivityConfirmed || state.ActiveEnrollID != 2 || state.PreviousActiveEnrollID != 1 || !state.StateStartedAt.Equal(now) {
		t.Fatalf("activity did not switch: %#v", state)
	}
}

func TestResolveSKCActivityStateConflictsWhenMultipleActivitiesConsume(t *testing.T) {
	previous := SKCActivityState{SKCID: 10, Status: SKCActivityConfirmed, ActiveEnrollID: 1}
	state := ResolveSKCActivityState(time.Now(), 10, []ActivityStockPoint{{EnrollID: 1, IntervalConsumed: 1}, {EnrollID: 2, IntervalConsumed: 2}}, &previous)
	if state.Status != SKCActivityConflict || len(state.EvidenceEnrollIDs) != 2 || state.ActiveEnrollID != 0 {
		t.Fatalf("simultaneous consumption was not marked conflict: %#v", state)
	}
}

func TestResolveSKCActivityStateUnknownWithoutEvidence(t *testing.T) {
	state := ResolveSKCActivityState(time.Now(), 10, []ActivityStockPoint{{EnrollID: 1}, {EnrollID: 2}}, nil)
	if state.Status != SKCActivityUnknown || state.ActiveEnrollID != 0 {
		t.Fatalf("unexpected no-evidence state: %#v", state)
	}
}
