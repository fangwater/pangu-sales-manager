package main

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/lib/pq"

	"pangu-sales-manager/internal/marketing"
)

func (s *Store) updateTemuSKUPriceIntervals(ctx context.Context, capturedAt time.Time, states []marketing.SKUPriceState) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT id,sku_id,skc_id,status,active_enroll_id,candidate_enroll_ids,
			currency,daily_price,activity_price,price,price_source,reason,
			start_at,update_at,end_at,
			EXTRACT(EPOCH FROM (COALESCE(end_at,update_at)-start_at))::bigint
		FROM temu_sku_price_intervals WHERE end_at IS NULL FOR UPDATE
	`)
	if err != nil {
		return err
	}
	open := make(map[int64]marketing.SKUPriceInterval)
	for rows.Next() {
		interval, err := scanSKUPriceInterval(rows)
		if err != nil {
			rows.Close()
			return err
		}
		open[interval.SKUID] = interval
	}
	if err := rows.Close(); err != nil {
		return err
	}

	current := make(map[int64]marketing.SKUPriceState, len(states))
	for _, state := range states {
		if state.Status == marketing.SKCActivityConfirmed {
			state.Reason = ""
		}
		current[state.SKUID] = state
		if existing, ok := open[state.SKUID]; ok {
			if sameSKUPriceInterval(existing, state) {
				if _, err := tx.ExecContext(ctx, `
					UPDATE temu_sku_price_intervals SET update_at=$2 WHERE id=$1
				`, existing.ID, capturedAt); err != nil {
					return fmt.Errorf("extend SKU price interval: %w", err)
				}
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE temu_sku_price_intervals SET update_at=$2,end_at=$2 WHERE id=$1
			`, existing.ID, capturedAt); err != nil {
				return fmt.Errorf("close changed SKU price interval: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO temu_sku_price_intervals(
				sku_id,skc_id,status,active_enroll_id,candidate_enroll_ids,
				currency,daily_price,activity_price,price,price_source,reason,start_at,update_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
		`, state.SKUID, state.SKCID, state.Status, nullablePositiveInt64(state.ActiveEnrollID),
			pq.Array(nonNilInt64s(state.CandidateEnrollIDs)), state.Currency, state.DailyPrice,
			state.ActivityPrice, state.ResolvedPrice, state.PriceSource, state.Reason, capturedAt); err != nil {
			return fmt.Errorf("insert SKU price interval: %w", err)
		}
	}
	for skuID, existing := range open {
		if _, ok := current[skuID]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE temu_sku_price_intervals SET update_at=$2,end_at=$2 WHERE id=$1
		`, existing.ID, capturedAt); err != nil {
			return fmt.Errorf("close missing SKU price interval: %w", err)
		}
	}
	return tx.Commit()
}

func sameSKUPriceInterval(existing marketing.SKUPriceInterval, state marketing.SKUPriceState) bool {
	return existing.SKCID == state.SKCID && existing.Status == state.Status &&
		existing.ActiveEnrollID == state.ActiveEnrollID && slices.Equal(existing.CandidateEnrollIDs, state.CandidateEnrollIDs) &&
		existing.Currency == state.Currency && existing.DailyPrice == state.DailyPrice &&
		existing.ActivityPrice == state.ActivityPrice && existing.Price == state.ResolvedPrice &&
		existing.PriceSource == state.PriceSource && existing.Reason == state.Reason
}

func (s *Store) latestSKUPriceStates(ctx context.Context, skuID, skcID int64, status string) ([]marketing.SKUPriceInterval, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,sku_id,skc_id,status,active_enroll_id,candidate_enroll_ids,
			currency,daily_price,activity_price,price,price_source,reason,
			start_at,update_at,end_at,
			EXTRACT(EPOCH FROM (COALESCE(end_at,update_at)-start_at))::bigint
		FROM temu_sku_price_intervals
		WHERE end_at IS NULL AND ($1::bigint=0 OR sku_id=$1::bigint)
		  AND ($2::bigint=0 OR skc_id=$2::bigint) AND ($3::text='' OR status=$3::text)
		ORDER BY sku_id
	`, skuID, skcID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make([]marketing.SKUPriceInterval, 0)
	for rows.Next() {
		state, err := scanSKUPriceInterval(rows)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func (s *Store) skuPriceStateHistory(ctx context.Context, skuID int64, limit int) ([]marketing.SKUPriceInterval, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,sku_id,skc_id,status,active_enroll_id,candidate_enroll_ids,
			currency,daily_price,activity_price,price,price_source,reason,
			start_at,update_at,end_at,
			EXTRACT(EPOCH FROM (COALESCE(end_at,update_at)-start_at))::bigint
		FROM temu_sku_price_intervals WHERE sku_id=$1 ORDER BY start_at DESC LIMIT $2
	`, skuID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make([]marketing.SKUPriceInterval, 0)
	for rows.Next() {
		state, err := scanSKUPriceInterval(rows)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSKUPriceInterval(row rowScanner) (marketing.SKUPriceInterval, error) {
	var state marketing.SKUPriceInterval
	var active sql.NullInt64
	var candidates pq.Int64Array
	var end sql.NullTime
	err := row.Scan(&state.ID, &state.SKUID, &state.SKCID, &state.Status, &active,
		&candidates, &state.Currency, &state.DailyPrice, &state.ActivityPrice,
		&state.Price, &state.PriceSource, &state.Reason, &state.StartAt, &state.UpdateAt,
		&end, &state.DurationSeconds)
	if active.Valid {
		state.ActiveEnrollID = active.Int64
	}
	state.CandidateEnrollIDs = append([]int64(nil), candidates...)
	if end.Valid {
		state.EndAt = &end.Time
	}
	return state, err
}

func nullablePositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nonNilInt64s(values []int64) []int64 {
	if values == nil {
		return []int64{}
	}
	return values
}
