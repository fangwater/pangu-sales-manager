package marketing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"pangu-sales-manager/internal/temu"
)

const (
	enrollmentPageSize = 50
	goodsPageSize      = 100
	goodsSKCBatchSize  = 50
	maxRetryAttempts   = 3
)

// Snapshot is the latest complete activity pull. It is intentionally kept in
// process memory and rebuilt after every service restart.
type Snapshot struct {
	StartedAt       time.Time                              `json:"started_at"`
	CompletedAt     time.Time                              `json:"completed_at"`
	LastError       string                                 `json:"last_error,omitempty"`
	Activities      []temu.MarketingActivity               `json:"activities"`
	Enrollments     []temu.MarketingEnrollment             `json:"enrollments"`
	DetailsByType   map[int64]temu.MarketingActivityDetail `json:"details_by_type"`
	DetailErrors    map[int64]string                       `json:"detail_errors,omitempty"`
	EnrollmentPages int                                    `json:"enrollment_pages"`
	GoodsBySKC      map[int64]temu.GoodsSummary            `json:"goods_by_skc"`
	GoodsPages      int                                    `json:"goods_pages"`
}

func (s Snapshot) Successful() bool {
	return !s.StartedAt.IsZero() && !s.CompletedAt.IsZero() && s.LastError == ""
}

type Syncer struct {
	client *temu.Client

	runMu      sync.Mutex
	snapshotMu sync.RWMutex
	snapshot   Snapshot
	now        func() time.Time
	wait       func(context.Context, time.Duration) error
}

func NewSyncer(client *temu.Client) *Syncer {
	return &Syncer{client: client, now: time.Now, wait: waitContext}
}

func (s *Syncer) Snapshot() Snapshot {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return cloneSnapshot(s.snapshot)
}

func (s *Syncer) Sync(ctx context.Context) (Snapshot, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	snapshot := Snapshot{
		StartedAt: s.now().UTC(), DetailsByType: make(map[int64]temu.MarketingActivityDetail),
		DetailErrors: make(map[int64]string), GoodsBySKC: make(map[int64]temu.GoodsSummary),
	}
	activities, err := retry(ctx, s.wait, func() (temu.MarketingActivityListResult, error) {
		return s.client.MarketingActivities(ctx)
	})
	if err != nil {
		return s.finish(snapshot, err)
	}
	snapshot.Activities = activities.ActivityList

	activityTypes := make(map[int64]struct{}, len(activities.ActivityList))
	for _, activity := range activities.ActivityList {
		if activity.ActivityType != 0 {
			activityTypes[activity.ActivityType] = struct{}{}
		}
	}
	for pageNo := 1; ; pageNo++ {
		page, pageErr := retry(ctx, s.wait, func() (temu.MarketingEnrollmentListResult, error) {
			return s.client.MarketingEnrollmentPage(ctx, pageNo, enrollmentPageSize)
		})
		if pageErr != nil {
			return s.finish(snapshot, pageErr)
		}
		snapshot.EnrollmentPages++
		snapshot.Enrollments = append(snapshot.Enrollments, page.List...)
		for _, enrollment := range page.List {
			if enrollment.ActivityType != 0 {
				activityTypes[enrollment.ActivityType] = struct{}{}
			}
		}
		if len(snapshot.Enrollments) >= page.Total || len(page.List) < enrollmentPageSize {
			break
		}
	}
	if err := s.syncCurrentGoods(ctx, &snapshot); err != nil {
		return s.finish(snapshot, err)
	}

	types := make([]int64, 0, len(activityTypes))
	for activityType := range activityTypes {
		types = append(types, activityType)
	}
	sort.Slice(types, func(left, right int) bool { return types[left] < types[right] })
	for _, activityType := range types {
		detail, detailErr := retry(ctx, s.wait, func() (temu.MarketingActivityDetail, error) {
			return s.client.MarketingActivityDetail(ctx, activityType)
		})
		if detailErr != nil {
			snapshot.DetailErrors[activityType] = detailErr.Error()
			continue
		}
		snapshot.DetailsByType[activityType] = detail
	}

	snapshot.CompletedAt = s.now().UTC()
	s.publish(snapshot)
	return cloneSnapshot(snapshot), nil
}

func (s *Syncer) syncCurrentGoods(ctx context.Context, snapshot *Snapshot) error {
	for _, batch := range batches(currentSKCIDs(snapshot.Enrollments), goodsSKCBatchSize) {
		received := 0
		for pageNo := 1; ; pageNo++ {
			page, err := retry(ctx, s.wait, func() (temu.GoodsListResult, error) {
				return s.client.GoodsPage(ctx, batch, pageNo, goodsPageSize)
			})
			if err != nil {
				return err
			}
			snapshot.GoodsPages++
			received += len(page.Data)
			for _, goods := range page.Data {
				if goods.ProductSKCID != 0 {
					snapshot.GoodsBySKC[goods.ProductSKCID] = goods
				}
			}
			if received >= page.TotalCount || len(page.Data) < goodsPageSize {
				break
			}
		}
	}
	return nil
}

func currentSKCIDs(enrollments []temu.MarketingEnrollment) []int64 {
	ids := make(map[int64]struct{})
	for _, enrollment := range enrollments {
		current := false
		for _, session := range enrollment.AssignedSessions {
			if session.SessionStatus == 2 {
				current = true
				break
			}
		}
		if !current {
			continue
		}
		for _, skc := range enrollment.SKCList {
			if skc.SKCID != 0 {
				ids[skc.SKCID] = struct{}{}
			}
		}
	}
	values := make([]int64, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	return values
}

func batches(values []int64, size int) [][]int64 {
	result := make([][]int64, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := min(start+size, len(values))
		result = append(result, values[start:end])
	}
	return result
}

func (s *Syncer) finish(snapshot Snapshot, err error) (Snapshot, error) {
	snapshot.CompletedAt = s.now().UTC()
	snapshot.LastError = err.Error()
	s.snapshotMu.Lock()
	if !s.snapshot.Successful() {
		s.snapshot = cloneSnapshot(snapshot)
	}
	s.snapshotMu.Unlock()
	return cloneSnapshot(snapshot), err
}

func (s *Syncer) publish(snapshot Snapshot) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.snapshot = cloneSnapshot(snapshot)
}

func retry[T any](ctx context.Context, wait func(context.Context, time.Duration) error, request func() (T, error)) (T, error) {
	var zero T
	for attempt := 0; attempt < maxRetryAttempts; attempt++ {
		result, err := request()
		if err == nil {
			return result, nil
		}
		if !retryable(err) || attempt == maxRetryAttempts-1 {
			return zero, err
		}
		if err := wait(ctx, time.Second<<attempt); err != nil {
			return zero, err
		}
	}
	return zero, errors.New("marketing retry attempts exhausted")
}

func retryable(err error) bool {
	var apiErr *temu.APIError
	return errors.As(err, &apiErr) && (apiErr.Temporary || temu.IsRateLimitError(apiErr))
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func cloneSnapshot(source Snapshot) Snapshot {
	copy := source
	copy.Activities = append([]temu.MarketingActivity(nil), source.Activities...)
	copy.Enrollments = append([]temu.MarketingEnrollment(nil), source.Enrollments...)
	copy.DetailsByType = make(map[int64]temu.MarketingActivityDetail, len(source.DetailsByType))
	for key, value := range source.DetailsByType {
		copy.DetailsByType[key] = value
	}
	copy.DetailErrors = make(map[int64]string, len(source.DetailErrors))
	for key, value := range source.DetailErrors {
		copy.DetailErrors[key] = value
	}
	copy.GoodsBySKC = make(map[int64]temu.GoodsSummary, len(source.GoodsBySKC))
	for key, value := range source.GoodsBySKC {
		value.ProductSKUSummaries = append([]temu.GoodsSKUInfo(nil), value.ProductSKUSummaries...)
		copy.GoodsBySKC[key] = value
	}
	return copy
}

func (s Snapshot) String() string {
	return fmt.Sprintf("activities=%d enrollments=%d pages=%d goods_skcs=%d goods_pages=%d details=%d detail_errors=%d", len(s.Activities), len(s.Enrollments), s.EnrollmentPages, len(s.GoodsBySKC), s.GoodsPages, len(s.DetailsByType), len(s.DetailErrors))
}
