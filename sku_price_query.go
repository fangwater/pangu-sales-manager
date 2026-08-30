package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"

	"pangu-sales-manager/internal/marketing"
)

const maxSKUPriceQueryItems = 1000

type SKUPriceQueryItem struct {
	SKUID int64      `json:"sku_id"`
	At    *time.Time `json:"at,omitempty"`
}

type SKUPriceQueryRequest struct {
	Items []SKUPriceQueryItem `json:"items"`
}

type SKUPriceQueryResult struct {
	Index              int        `json:"index"`
	SKUID              int64      `json:"sku_id"`
	SKCID              int64      `json:"skc_id,omitempty"`
	RequestedAt        *time.Time `json:"requested_at,omitempty"`
	ObservedAt         *time.Time `json:"observed_at,omitempty"`
	Status             string     `json:"status"`
	Price              int64      `json:"price"`
	Currency           string     `json:"currency"`
	PriceSource        string     `json:"price_source"`
	ActiveEnrollID     int64      `json:"active_enroll_id,omitempty"`
	CandidateEnrollIDs []int64    `json:"candidate_enroll_ids,omitempty"`
	MatchMethod        string     `json:"match_method"`
	IntervalID         int64      `json:"interval_id,omitempty"`
	IntervalStartAt    *time.Time `json:"interval_start_at,omitempty"`
	IntervalEndAt      *time.Time `json:"interval_end_at,omitempty"`
	DistanceSeconds    int64      `json:"distance_seconds,omitempty"`
	Reason             string     `json:"reason,omitempty"`
}

type historicalSKUPriceRequest struct {
	RequestIndex int       `json:"request_index"`
	SKUID        int64     `json:"sku_id"`
	RequestedAt  time.Time `json:"requested_at"`
}

func resolveSKUPriceQueries(ctx context.Context, observer *marketing.ActivityObserver, store *Store, items []SKUPriceQueryItem) ([]SKUPriceQueryResult, error) {
	if len(items) == 0 || len(items) > maxSKUPriceQueryItems {
		return nil, fmt.Errorf("items must contain between 1 and %d entries", maxSKUPriceQueryItems)
	}
	for _, item := range items {
		if item.SKUID <= 0 {
			return nil, errors.New("sku_id must be a positive integer")
		}
	}

	results := make([]SKUPriceQueryResult, len(items))
	historical := make([]historicalSKUPriceRequest, 0)
	latest := observer.Latest()
	currentBySKU := make(map[int64]marketing.SKUPriceState, len(latest.SKUPrices))
	for _, price := range latest.SKUPrices {
		currentBySKU[price.SKUID] = price
	}
	for index, item := range items {
		if item.At != nil {
			historical = append(historical, historicalSKUPriceRequest{RequestIndex: index, SKUID: item.SKUID, RequestedAt: item.At.UTC()})
			continue
		}
		result := SKUPriceQueryResult{Index: index, SKUID: item.SKUID, Status: marketing.SKCActivityWarning, PriceSource: marketing.SKUPriceSourceUnresolved, MatchMethod: "current_memory"}
		if !latest.CapturedAt.IsZero() {
			result.ObservedAt = timePointerCopy(latest.CapturedAt)
		}
		if price, ok := currentBySKU[item.SKUID]; ok {
			result.SKCID = price.SKCID
			result.Status = price.Status
			result.Price = price.ResolvedPrice
			result.Currency = price.Currency
			result.PriceSource = price.PriceSource
			result.ActiveEnrollID = price.ActiveEnrollID
			result.CandidateEnrollIDs = append([]int64(nil), price.CandidateEnrollIDs...)
			if price.Status == marketing.SKCActivityWarning {
				result.Reason = price.Reason
			}
		} else {
			result.Reason = "current_sku_not_found"
		}
		results[index] = result
	}
	if len(historical) > 0 {
		resolved, err := store.resolveHistoricalSKUPrices(ctx, historical)
		if err != nil {
			return nil, err
		}
		for _, result := range resolved {
			results[result.Index] = result
		}
	}
	return results, nil
}

func (s *Store) resolveHistoricalSKUPrices(ctx context.Context, requests []historicalSKUPriceRequest) ([]SKUPriceQueryResult, error) {
	payload, err := json.Marshal(requests)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH requested AS (
			SELECT request_index,sku_id,requested_at
			FROM jsonb_to_recordset($1::jsonb)
			AS value(request_index integer,sku_id bigint,requested_at timestamptz)
		)
		SELECT q.request_index,q.sku_id,q.requested_at,
			p.id,p.skc_id,p.status,p.active_enroll_id,p.candidate_enroll_ids,
			p.currency,p.price,p.price_source,p.reason,p.start_at,p.end_at,
			CASE
				WHEN p.id IS NULL THEN 'unmatched'
				WHEN q.requested_at>=p.start_at AND q.requested_at<COALESCE(p.end_at,'infinity') THEN 'exact_interval'
				WHEN q.requested_at<p.start_at THEN 'nearest_after'
				ELSE 'nearest_before'
			END AS match_method,
			CASE
				WHEN p.id IS NULL OR (q.requested_at>=p.start_at AND q.requested_at<COALESCE(p.end_at,'infinity')) THEN 0
				WHEN q.requested_at<p.start_at THEN EXTRACT(EPOCH FROM p.start_at-q.requested_at)::bigint
				ELSE EXTRACT(EPOCH FROM q.requested_at-COALESCE(p.end_at,p.update_at))::bigint
			END AS distance_seconds
		FROM requested q
		LEFT JOIN LATERAL (
			SELECT candidate.* FROM temu_sku_price_intervals candidate
			WHERE candidate.sku_id=q.sku_id
			ORDER BY
				CASE WHEN q.requested_at>=candidate.start_at AND q.requested_at<COALESCE(candidate.end_at,'infinity') THEN 0 ELSE 1 END,
				CASE WHEN q.requested_at<candidate.start_at THEN candidate.start_at-q.requested_at ELSE q.requested_at-COALESCE(candidate.end_at,candidate.update_at) END,
				candidate.start_at DESC
			LIMIT 1
		) p ON true
		ORDER BY q.request_index
	`, payload)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]SKUPriceQueryResult, 0, len(requests))
	for rows.Next() {
		var result SKUPriceQueryResult
		var intervalID, skcID, activeEnroll sql.NullInt64
		var status, currency, source, intervalReason, matchMethod sql.NullString
		var candidates pq.Int64Array
		var price, distance sql.NullInt64
		var startAt, endAt sql.NullTime
		if err := rows.Scan(&result.Index, &result.SKUID, &result.RequestedAt,
			&intervalID, &skcID, &status, &activeEnroll, &candidates,
			&currency, &price, &source, &intervalReason, &startAt, &endAt,
			&matchMethod, &distance); err != nil {
			return nil, err
		}
		result.Status = marketing.SKCActivityWarning
		result.PriceSource = marketing.SKUPriceSourceUnresolved
		result.MatchMethod = "unmatched"
		if !intervalID.Valid {
			result.Reason = "historical_sku_price_not_found"
			results = append(results, result)
			continue
		}
		result.IntervalID = intervalID.Int64
		result.SKCID = skcID.Int64
		result.ActiveEnrollID = activeEnroll.Int64
		result.CandidateEnrollIDs = append([]int64(nil), candidates...)
		result.Currency = currency.String
		result.Price = price.Int64
		result.PriceSource = source.String
		result.MatchMethod = matchMethod.String
		result.DistanceSeconds = distance.Int64
		if startAt.Valid {
			result.IntervalStartAt = &startAt.Time
		}
		if endAt.Valid {
			result.IntervalEndAt = &endAt.Time
		}
		if result.MatchMethod == "exact_interval" {
			result.Status = status.String
			if result.Status == marketing.SKCActivityWarning {
				result.Reason = intervalReason.String
			}
		} else {
			result.Status = marketing.SKCActivityWarning
			result.Reason = "requested_time_outside_observed_interval"
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func timePointerCopy(value time.Time) *time.Time {
	copy := value
	return &copy
}
