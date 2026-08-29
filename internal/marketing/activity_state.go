package marketing

import (
	"slices"
	"sort"
	"time"
)

const (
	SKCActivityConfirmed = "confirmed"
	SKCActivityWarning   = "warning"
)

type ActivityStockPoint struct {
	EnrollID           int64
	SKCID              int64
	CumulativeConsumed int64
	IntervalConsumed   int64
}

type SKCActivityState struct {
	SKCID                  int64      `json:"skc_id"`
	Status                 string     `json:"status"`
	ActiveEnrollID         int64      `json:"active_enroll_id,omitempty"`
	PreviousActiveEnrollID int64      `json:"previous_active_enroll_id,omitempty"`
	CandidateEnrollIDs     []int64    `json:"candidate_enroll_ids"`
	EvidenceEnrollIDs      []int64    `json:"evidence_enroll_ids"`
	StateStartedAt         time.Time  `json:"state_started_at"`
	LastEvidenceAt         *time.Time `json:"last_evidence_at,omitempty"`
	CarriedForward         bool       `json:"carried_forward"`
	Reason                 string     `json:"reason"`
	CapturedAt             time.Time  `json:"captured_at"`
}

func ResolveSKCActivityState(now time.Time, skcID int64, candidates []ActivityStockPoint, previous *SKCActivityState) SKCActivityState {
	state := SKCActivityState{SKCID: skcID, CapturedAt: now, StateStartedAt: now}
	state.CandidateEnrollIDs = uniqueEnrollIDs(candidates)
	if len(candidates) == 0 {
		state.Status = SKCActivityWarning
		state.Reason = "no_current_activity"
		if previous != nil && previous.Status == SKCActivityWarning && previous.Reason == "no_current_activity" {
			state.StateStartedAt = previous.StateStartedAt
			state.CarriedForward = true
		}
		return state
	}

	consuming := make([]int64, 0)
	for _, candidate := range candidates {
		if candidate.IntervalConsumed > 0 {
			consuming = append(consuming, candidate.EnrollID)
		}
	}
	consuming = uniqueSorted(consuming)
	if len(consuming) == 1 {
		state.Status = SKCActivityConfirmed
		state.ActiveEnrollID = consuming[0]
		state.EvidenceEnrollIDs = consuming
		state.LastEvidenceAt = timePointer(now)
		state.Reason = "interval_unique_consumption"
		if previous != nil {
			state.PreviousActiveEnrollID = previous.ActiveEnrollID
			if previous.Status == SKCActivityConfirmed && previous.ActiveEnrollID == state.ActiveEnrollID {
				state.StateStartedAt = previous.StateStartedAt
			}
		}
		return state
	}
	if len(consuming) > 1 {
		state.Status = SKCActivityWarning
		state.EvidenceEnrollIDs = consuming
		state.Reason = "interval_multiple_consumption"
		if previous != nil {
			state.PreviousActiveEnrollID = previous.ActiveEnrollID
		}
		return state
	}

	if previous != nil {
		state.PreviousActiveEnrollID = previous.ActiveEnrollID
		if previous.Status == SKCActivityConfirmed && slices.Contains(state.CandidateEnrollIDs, previous.ActiveEnrollID) {
			state.Status = SKCActivityConfirmed
			state.ActiveEnrollID = previous.ActiveEnrollID
			state.EvidenceEnrollIDs = append([]int64(nil), previous.EvidenceEnrollIDs...)
			state.StateStartedAt = previous.StateStartedAt
			state.LastEvidenceAt = previous.LastEvidenceAt
			state.CarriedForward = true
			state.Reason = "carry_forward_no_consumption"
			return state
		}
		if previous.Status == SKCActivityWarning && len(previous.EvidenceEnrollIDs) > 1 {
			evidence := intersectIDs(previous.EvidenceEnrollIDs, state.CandidateEnrollIDs)
			if len(evidence) > 1 {
				state.Status = SKCActivityWarning
				state.EvidenceEnrollIDs = evidence
				state.StateStartedAt = previous.StateStartedAt
				state.LastEvidenceAt = previous.LastEvidenceAt
				state.CarriedForward = true
				state.Reason = "carry_forward_conflict"
				return state
			}
		}
		if previous.Status == SKCActivityWarning && equalIDs(previous.CandidateEnrollIDs, state.CandidateEnrollIDs) {
			state.Status = SKCActivityWarning
			state.StateStartedAt = previous.StateStartedAt
			state.EvidenceEnrollIDs = append([]int64(nil), previous.EvidenceEnrollIDs...)
			state.LastEvidenceAt = previous.LastEvidenceAt
			state.CarriedForward = true
			state.Reason = "carry_forward_warning"
			return state
		}
	}

	return resolveInitialState(state, candidates)
}

func resolveInitialState(state SKCActivityState, candidates []ActivityStockPoint) SKCActivityState {
	positive := make([]int64, 0)
	for _, candidate := range candidates {
		if candidate.CumulativeConsumed > 0 {
			positive = append(positive, candidate.EnrollID)
		}
	}
	positive = uniqueSorted(positive)
	state.EvidenceEnrollIDs = positive
	if len(positive) == 1 {
		state.Status = SKCActivityConfirmed
		state.ActiveEnrollID = positive[0]
		state.LastEvidenceAt = timePointer(state.CapturedAt)
		state.Reason = "initial_unique_cumulative_consumption"
		return state
	}
	if len(positive) > 1 {
		state.Status = SKCActivityWarning
		state.LastEvidenceAt = timePointer(state.CapturedAt)
		state.Reason = "initial_multiple_cumulative_consumption"
		return state
	}
	state.Status = SKCActivityWarning
	state.Reason = "initial_no_consumption"
	return state
}

func uniqueEnrollIDs(points []ActivityStockPoint) []int64 {
	values := make([]int64, 0, len(points))
	for _, point := range points {
		values = append(values, point.EnrollID)
	}
	return uniqueSorted(values)
}

func uniqueSorted(values []int64) []int64 {
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	return slices.Compact(values)
}

func intersectIDs(left, right []int64) []int64 {
	values := make([]int64, 0)
	for _, value := range left {
		if slices.Contains(right, value) {
			values = append(values, value)
		}
	}
	return uniqueSorted(values)
}

func equalIDs(left, right []int64) bool {
	return slices.Equal(uniqueSorted(append([]int64(nil), left...)), uniqueSorted(append([]int64(nil), right...)))
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}
