package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/lib/pq"

	"pangu-sales-manager/internal/marketing"
)

type activityEnrollmentKey struct {
	EnrollID int64
	SKCID    int64
}

type activityEnrollmentObservation struct {
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

type activityObservationView struct {
	SnapshotID  int64
	CapturedAt  time.Time
	Enrollments map[activityEnrollmentKey]activityEnrollmentObservation
	States      map[int64]marketing.SKCActivityState
}

type enrollmentSnapshotRecord struct {
	EnrollID             int64
	SKCID                int64
	ActivityType         int64
	ActivityTypeName     string
	ActivityThematicID   int64
	ActivityThematicName string
	ActivityStock        int64
	RemainingStock       int64
	PreviousRemaining    *int64
	IntervalConsumed     int64
	IntervalIncreased    int64
	CumulativeConsumed   int64
	SKUCount             int
}

type skuPriceSnapshotRecord struct {
	EnrollID      int64
	SKCID         int64
	SKUID         int64
	SiteID        int64
	SessionID     int64
	Currency      string
	DailyPrice    int64
	ActivityPrice int64
}

func (s *Store) recordTemuActivitySnapshot(ctx context.Context, snapshot marketing.Snapshot) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var snapshotID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO temu_activity_snapshot_runs(captured_at,started_at,enrollment_count)
		VALUES ($1,$2,$3)
		ON CONFLICT (captured_at) DO NOTHING
		RETURNING id
	`, snapshot.CompletedAt, snapshot.StartedAt, len(snapshot.Enrollments)).Scan(&snapshotID)
	if err != nil {
		if err != sql.ErrNoRows {
			return 0, fmt.Errorf("insert activity snapshot run: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM temu_activity_snapshot_runs WHERE captured_at=$1`, snapshot.CompletedAt).Scan(&snapshotID); err != nil {
			return 0, err
		}
		return snapshotID, tx.Commit()
	}

	previousRemaining, err := loadPreviousEnrollmentStock(ctx, tx, snapshot.CompletedAt)
	if err != nil {
		return 0, err
	}
	previousStates, err := loadPreviousSKCStates(ctx, tx, snapshot.CompletedAt)
	if err != nil {
		return 0, err
	}

	enrollmentRecords, priceRecords := flattenActivitySnapshot(snapshot, previousRemaining)
	for _, record := range enrollmentRecords {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO temu_activity_enrollment_snapshots(
				snapshot_id,enroll_id,skc_id,activity_type,activity_type_name,
				activity_thematic_id,activity_thematic_name,activity_stock,
				remaining_activity_stock,previous_remaining_activity_stock,
				interval_consumed_stock,interval_increased_stock,
				cumulative_consumed_stock,enrollment_sku_count
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		`, snapshotID, record.EnrollID, record.SKCID, record.ActivityType, record.ActivityTypeName,
			record.ActivityThematicID, record.ActivityThematicName, record.ActivityStock,
			record.RemainingStock, nullableInt64Pointer(record.PreviousRemaining), record.IntervalConsumed,
			record.IntervalIncreased, record.CumulativeConsumed, record.SKUCount)
		if err != nil {
			return 0, fmt.Errorf("insert activity enrollment snapshot: %w", err)
		}
	}
	for _, record := range priceRecords {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO temu_activity_sku_price_snapshots(
				snapshot_id,enroll_id,skc_id,sku_id,site_id,session_id,
				currency,daily_price,activity_price
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT DO NOTHING
		`, snapshotID, record.EnrollID, record.SKCID, record.SKUID, record.SiteID,
			record.SessionID, record.Currency, record.DailyPrice, record.ActivityPrice)
		if err != nil {
			return 0, fmt.Errorf("insert activity SKU price snapshot: %w", err)
		}
	}

	pointsBySKC := make(map[int64][]marketing.ActivityStockPoint)
	for _, record := range enrollmentRecords {
		pointsBySKC[record.SKCID] = append(pointsBySKC[record.SKCID], marketing.ActivityStockPoint{
			EnrollID: record.EnrollID, SKCID: record.SKCID,
			CumulativeConsumed: record.CumulativeConsumed, IntervalConsumed: record.IntervalConsumed,
		})
	}
	stateSKCs := make(map[int64]struct{}, len(pointsBySKC)+len(previousStates))
	for skcID := range pointsBySKC {
		stateSKCs[skcID] = struct{}{}
	}
	for skcID, previous := range previousStates {
		if previous.Reason != "no_current_activity" {
			stateSKCs[skcID] = struct{}{}
		}
	}
	skcIDs := make([]int64, 0, len(stateSKCs))
	for skcID := range stateSKCs {
		skcIDs = append(skcIDs, skcID)
	}
	sort.Slice(skcIDs, func(left, right int) bool { return skcIDs[left] < skcIDs[right] })
	statesBySKC := make(map[int64]marketing.SKCActivityState, len(skcIDs))
	for _, skcID := range skcIDs {
		var previous *marketing.SKCActivityState
		if value, ok := previousStates[skcID]; ok {
			copy := value
			previous = &copy
		}
		state := marketing.ResolveSKCActivityState(snapshot.CompletedAt, skcID, pointsBySKC[skcID], previous)
		statesBySKC[skcID] = state
		_, err := tx.ExecContext(ctx, `
			INSERT INTO temu_skc_activity_state_snapshots(
				snapshot_id,skc_id,status,active_enroll_id,previous_active_enroll_id,
				candidate_enroll_ids,evidence_enroll_ids,state_started_at,
				last_evidence_at,carried_forward,reason
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		`, snapshotID, state.SKCID, state.Status, nullablePositiveInt64(state.ActiveEnrollID),
			nullablePositiveInt64(state.PreviousActiveEnrollID), pq.Array(nonNilInt64s(state.CandidateEnrollIDs)),
			pq.Array(nonNilInt64s(state.EvidenceEnrollIDs)), state.StateStartedAt, nullableTimePointer(state.LastEvidenceAt),
			state.CarriedForward, state.Reason)
		if err != nil {
			return 0, fmt.Errorf("insert SKC activity state: %w", err)
		}
	}

	priceStates := resolveSKUPriceSnapshots(snapshot.CompletedAt, priceRecords, statesBySKC)
	for _, state := range priceStates {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO temu_sku_price_state_snapshots(
				snapshot_id,sku_id,skc_id,site_id,status,active_enroll_id,
				candidate_enroll_ids,currency,daily_price,activity_price,
				resolved_price,price_source,reason
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		`, snapshotID, state.SKUID, state.SKCID, state.SiteID, state.Status,
			nullablePositiveInt64(state.ActiveEnrollID), pq.Array(nonNilInt64s(state.CandidateEnrollIDs)),
			state.Currency, state.DailyPrice, state.ActivityPrice, state.ResolvedPrice,
			state.PriceSource, state.Reason)
		if err != nil {
			return 0, fmt.Errorf("insert SKU price state: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return snapshotID, nil
}

func loadPreviousEnrollmentStock(ctx context.Context, tx *sql.Tx, before time.Time) (map[activityEnrollmentKey]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT ON (e.enroll_id,e.skc_id)
			e.enroll_id,e.skc_id,e.remaining_activity_stock
		FROM temu_activity_enrollment_snapshots e
		JOIN temu_activity_snapshot_runs r ON r.id=e.snapshot_id
		WHERE r.captured_at < $1
		ORDER BY e.enroll_id,e.skc_id,r.captured_at DESC
	`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make(map[activityEnrollmentKey]int64)
	for rows.Next() {
		var key activityEnrollmentKey
		var remaining int64
		if err := rows.Scan(&key.EnrollID, &key.SKCID, &remaining); err != nil {
			return nil, err
		}
		values[key] = remaining
	}
	return values, rows.Err()
}

func loadPreviousSKCStates(ctx context.Context, tx *sql.Tx, before time.Time) (map[int64]marketing.SKCActivityState, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT ON (s.skc_id)
			s.skc_id,s.status,s.active_enroll_id,s.previous_active_enroll_id,
			s.candidate_enroll_ids,s.evidence_enroll_ids,s.state_started_at,
			s.last_evidence_at,s.carried_forward,s.reason,r.captured_at
		FROM temu_skc_activity_state_snapshots s
		JOIN temu_activity_snapshot_runs r ON r.id=s.snapshot_id
		WHERE r.captured_at < $1
		ORDER BY s.skc_id,r.captured_at DESC
	`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make(map[int64]marketing.SKCActivityState)
	for rows.Next() {
		state, err := scanSKCActivityState(rows)
		if err != nil {
			return nil, err
		}
		values[state.SKCID] = state
	}
	return values, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSKCActivityState(row rowScanner) (marketing.SKCActivityState, error) {
	var state marketing.SKCActivityState
	var active, previous sql.NullInt64
	var candidates, evidence pq.Int64Array
	var lastEvidence sql.NullTime
	err := row.Scan(&state.SKCID, &state.Status, &active, &previous, &candidates, &evidence,
		&state.StateStartedAt, &lastEvidence, &state.CarriedForward, &state.Reason, &state.CapturedAt)
	if active.Valid {
		state.ActiveEnrollID = active.Int64
	}
	if previous.Valid {
		state.PreviousActiveEnrollID = previous.Int64
	}
	state.CandidateEnrollIDs = append([]int64(nil), candidates...)
	state.EvidenceEnrollIDs = append([]int64(nil), evidence...)
	if lastEvidence.Valid {
		state.LastEvidenceAt = &lastEvidence.Time
	}
	return state, err
}

func flattenActivitySnapshot(snapshot marketing.Snapshot, previous map[activityEnrollmentKey]int64) ([]enrollmentSnapshotRecord, []skuPriceSnapshotRecord) {
	enrollments := make([]enrollmentSnapshotRecord, 0, len(snapshot.Enrollments))
	prices := make([]skuPriceSnapshotRecord, 0)
	for _, enrollment := range snapshot.Enrollments {
		activityName := enrollment.ActivityThematicName
		if activityName == "" {
			activityName = enrollment.ActivityTypeName
		}
		for _, skc := range enrollment.SKCList {
			key := activityEnrollmentKey{EnrollID: enrollment.EnrollID, SKCID: skc.SKCID}
			record := enrollmentSnapshotRecord{
				EnrollID: enrollment.EnrollID, SKCID: skc.SKCID,
				ActivityType: enrollment.ActivityType, ActivityTypeName: enrollment.ActivityTypeName,
				ActivityThematicID: enrollment.ActivityThematicID, ActivityThematicName: activityName,
				ActivityStock: enrollment.ActivityStock, RemainingStock: enrollment.RemainingActivityStock,
				CumulativeConsumed: max(enrollment.ActivityStock-enrollment.RemainingActivityStock, 0),
				SKUCount:           len(skc.SKUList),
			}
			if value, ok := previous[key]; ok {
				record.PreviousRemaining = int64Pointer(value)
				if value > record.RemainingStock {
					record.IntervalConsumed = value - record.RemainingStock
				} else if value < record.RemainingStock {
					record.IntervalIncreased = record.RemainingStock - value
				}
			}
			enrollments = append(enrollments, record)
			for _, sku := range skc.SKUList {
				for _, price := range sku.SitePriceList {
					matched := false
					for _, session := range enrollment.AssignedSessions {
						if session.SiteID != 0 && price.SiteID != 0 && session.SiteID != price.SiteID {
							continue
						}
						prices = append(prices, skuPriceSnapshotRecord{
							EnrollID: enrollment.EnrollID, SKCID: skc.SKCID, SKUID: sku.SKUID,
							SiteID: price.SiteID, SessionID: session.SessionID,
							Currency:   firstNonEmptyString(sku.Currency, skc.Currency, enrollment.Currency),
							DailyPrice: price.DailyPrice, ActivityPrice: price.ActivityPrice,
						})
						matched = true
					}
					if !matched {
						prices = append(prices, skuPriceSnapshotRecord{
							EnrollID: enrollment.EnrollID, SKCID: skc.SKCID, SKUID: sku.SKUID,
							SiteID: price.SiteID, Currency: firstNonEmptyString(sku.Currency, skc.Currency, enrollment.Currency),
							DailyPrice: price.DailyPrice, ActivityPrice: price.ActivityPrice,
						})
					}
				}
			}
		}
	}
	return enrollments, prices
}

func (s *Store) activityObservationForSnapshot(ctx context.Context, capturedAt time.Time) (activityObservationView, error) {
	view := activityObservationView{Enrollments: make(map[activityEnrollmentKey]activityEnrollmentObservation), States: make(map[int64]marketing.SKCActivityState)}
	err := s.db.QueryRowContext(ctx, `
		SELECT id,captured_at FROM temu_activity_snapshot_runs
		WHERE captured_at <= $1 ORDER BY captured_at DESC LIMIT 1
	`, capturedAt).Scan(&view.SnapshotID, &view.CapturedAt)
	if err != nil {
		return view, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT enroll_id,skc_id,activity_stock,remaining_activity_stock,
			previous_remaining_activity_stock,interval_consumed_stock,
			interval_increased_stock,cumulative_consumed_stock
		FROM temu_activity_enrollment_snapshots WHERE snapshot_id=$1
	`, view.SnapshotID)
	if err != nil {
		return view, err
	}
	for rows.Next() {
		var observation activityEnrollmentObservation
		var previous sql.NullInt64
		if err := rows.Scan(&observation.EnrollID, &observation.SKCID, &observation.ActivityStock,
			&observation.RemainingActivityStock, &previous, &observation.IntervalConsumedStock,
			&observation.IntervalIncreasedStock, &observation.CumulativeConsumedStock); err != nil {
			rows.Close()
			return view, err
		}
		if previous.Valid {
			observation.PreviousRemainingActivityStock = int64Pointer(previous.Int64)
		}
		observation.CapturedAt = view.CapturedAt
		view.Enrollments[activityEnrollmentKey{EnrollID: observation.EnrollID, SKCID: observation.SKCID}] = observation
	}
	if err := rows.Close(); err != nil {
		return view, err
	}
	stateRows, err := s.db.QueryContext(ctx, `
		SELECT s.skc_id,s.status,s.active_enroll_id,s.previous_active_enroll_id,
			s.candidate_enroll_ids,s.evidence_enroll_ids,s.state_started_at,
			s.last_evidence_at,s.carried_forward,s.reason,r.captured_at
		FROM temu_skc_activity_state_snapshots s
		JOIN temu_activity_snapshot_runs r ON r.id=s.snapshot_id
		WHERE s.snapshot_id=$1
	`, view.SnapshotID)
	if err != nil {
		return view, err
	}
	defer stateRows.Close()
	for stateRows.Next() {
		state, err := scanSKCActivityState(stateRows)
		if err != nil {
			return view, err
		}
		view.States[state.SKCID] = state
	}
	return view, stateRows.Err()
}

func (s *Store) latestSKCActivityStates(ctx context.Context) ([]marketing.SKCActivityState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT ON (s.skc_id)
			s.skc_id,s.status,s.active_enroll_id,s.previous_active_enroll_id,
			s.candidate_enroll_ids,s.evidence_enroll_ids,s.state_started_at,
			s.last_evidence_at,s.carried_forward,s.reason,r.captured_at
		FROM temu_skc_activity_state_snapshots s
		JOIN temu_activity_snapshot_runs r ON r.id=s.snapshot_id
		ORDER BY s.skc_id,r.captured_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make([]marketing.SKCActivityState, 0)
	for rows.Next() {
		state, err := scanSKCActivityState(rows)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func (s *Store) skcActivityStateHistory(ctx context.Context, skcID int64, limit int) ([]marketing.SKCActivityState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.skc_id,s.status,s.active_enroll_id,s.previous_active_enroll_id,
			s.candidate_enroll_ids,s.evidence_enroll_ids,s.state_started_at,
			s.last_evidence_at,s.carried_forward,s.reason,r.captured_at
		FROM temu_skc_activity_state_snapshots s
		JOIN temu_activity_snapshot_runs r ON r.id=s.snapshot_id
		WHERE s.skc_id=$1
		ORDER BY r.captured_at DESC LIMIT $2
	`, skcID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make([]marketing.SKCActivityState, 0)
	for rows.Next() {
		state, err := scanSKCActivityState(rows)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

type skuPriceGroupKey struct {
	SKUID  int64
	SKCID  int64
	SiteID int64
}

func resolveSKUPriceSnapshots(capturedAt time.Time, records []skuPriceSnapshotRecord, states map[int64]marketing.SKCActivityState) []marketing.SKUPriceState {
	groups := make(map[skuPriceGroupKey][]marketing.SKUActivityPricePoint)
	for _, record := range records {
		key := skuPriceGroupKey{SKUID: record.SKUID, SKCID: record.SKCID, SiteID: record.SiteID}
		groups[key] = append(groups[key], marketing.SKUActivityPricePoint{
			EnrollID: record.EnrollID, SKCID: record.SKCID, SKUID: record.SKUID,
			SiteID: record.SiteID, Currency: record.Currency,
			DailyPrice: record.DailyPrice, ActivityPrice: record.ActivityPrice,
		})
	}
	keys := make([]skuPriceGroupKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].SKUID != keys[right].SKUID {
			return keys[left].SKUID < keys[right].SKUID
		}
		if keys[left].SKCID != keys[right].SKCID {
			return keys[left].SKCID < keys[right].SKCID
		}
		return keys[left].SiteID < keys[right].SiteID
	})
	results := make([]marketing.SKUPriceState, 0, len(keys))
	for _, key := range keys {
		results = append(results, marketing.ResolveSKUPriceState(capturedAt, states[key.SKCID], groups[key]))
	}
	return results
}

func (s *Store) latestSKUPriceStates(ctx context.Context, skuID, skcID, siteID int64, status string) ([]marketing.SKUPriceState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT ON (p.sku_id,p.skc_id,p.site_id)
			p.sku_id,p.skc_id,p.site_id,p.status,p.active_enroll_id,
			p.candidate_enroll_ids,p.currency,p.daily_price,p.activity_price,
			p.resolved_price,p.price_source,p.reason,r.captured_at
		FROM temu_sku_price_state_snapshots p
		JOIN temu_activity_snapshot_runs r ON r.id=p.snapshot_id
		WHERE ($1::bigint=0 OR p.sku_id=$1::bigint) AND ($2::bigint=0 OR p.skc_id=$2::bigint)
		  AND ($3::bigint=0 OR p.site_id=$3::bigint) AND ($4::text='' OR p.status=$4::text)
		ORDER BY p.sku_id,p.skc_id,p.site_id,r.captured_at DESC
	`, skuID, skcID, siteID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make([]marketing.SKUPriceState, 0)
	for rows.Next() {
		state, err := scanSKUPriceState(rows)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func (s *Store) skuPriceStateHistory(ctx context.Context, skuID int64, limit int) ([]marketing.SKUPriceState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.sku_id,p.skc_id,p.site_id,p.status,p.active_enroll_id,
			p.candidate_enroll_ids,p.currency,p.daily_price,p.activity_price,
			p.resolved_price,p.price_source,p.reason,r.captured_at
		FROM temu_sku_price_state_snapshots p
		JOIN temu_activity_snapshot_runs r ON r.id=p.snapshot_id
		WHERE p.sku_id=$1
		ORDER BY r.captured_at DESC,p.site_id LIMIT $2
	`, skuID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make([]marketing.SKUPriceState, 0)
	for rows.Next() {
		state, err := scanSKUPriceState(rows)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func scanSKUPriceState(row rowScanner) (marketing.SKUPriceState, error) {
	var state marketing.SKUPriceState
	var active sql.NullInt64
	var candidates pq.Int64Array
	err := row.Scan(&state.SKUID, &state.SKCID, &state.SiteID, &state.Status, &active,
		&candidates, &state.Currency, &state.DailyPrice, &state.ActivityPrice,
		&state.ResolvedPrice, &state.PriceSource, &state.Reason, &state.CapturedAt)
	if active.Valid {
		state.ActiveEnrollID = active.Int64
	}
	state.CandidateEnrollIDs = append([]int64(nil), candidates...)
	return state, err
}

func nullablePositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullableInt64Pointer(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTimePointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64Pointer(value int64) *int64 {
	copy := value
	return &copy
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func nonNilInt64s(values []int64) []int64 {
	if values == nil {
		return []int64{}
	}
	return values
}
