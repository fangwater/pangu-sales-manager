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
	StartedAt       time.Time                   `json:"started_at"`
	CompletedAt     time.Time                   `json:"completed_at"`
	LastError       string                      `json:"last_error,omitempty"`
	Enrollments     []temu.MarketingEnrollment  `json:"enrollments"`
	EnrollmentPages int                         `json:"enrollment_pages"`
	GoodsBySKC      map[int64]temu.GoodsSummary `json:"goods_by_skc"`
	GoodsPages      int                         `json:"goods_pages"`
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

	snapshot := Snapshot{StartedAt: s.now().UTC(), GoodsBySKC: make(map[int64]temu.GoodsSummary)}
	for pageNo := 1; ; pageNo++ {
		page, pageErr := retry(ctx, s.wait, func() (temu.MarketingEnrollmentListResult, error) {
			return s.client.CurrentMarketingEnrollmentPage(ctx, pageNo, enrollmentPageSize)
		})
		if pageErr != nil {
			return s.finish(snapshot, pageErr)
		}
		snapshot.EnrollmentPages++
		for _, enrollment := range page.List {
			if current, ok := currentEnrollment(enrollment); ok {
				snapshot.Enrollments = append(snapshot.Enrollments, current)
			}
		}
		if pageNo*enrollmentPageSize >= page.Total || len(page.List) < enrollmentPageSize {
			break
		}
	}
	if err := s.syncCurrentGoods(ctx, &snapshot); err != nil {
		return s.finish(snapshot, err)
	}

	snapshot.CompletedAt = s.now().UTC()
	s.publish(snapshot)
	return cloneSnapshot(snapshot), nil
}

func currentEnrollment(enrollment temu.MarketingEnrollment) (temu.MarketingEnrollment, bool) {
	activeSites := make(map[int64]struct{})
	sessions := enrollment.AssignedSessions[:0]
	for _, session := range enrollment.AssignedSessions {
		if session.SessionStatus != temu.CurrentSessionStatus {
			continue
		}
		sessions = append(sessions, session)
		activeSites[session.SiteID] = struct{}{}
	}
	if len(sessions) == 0 {
		return temu.MarketingEnrollment{}, false
	}
	enrollment.AssignedSessions = sessions
	for skcIndex := range enrollment.SKCList {
		for skuIndex := range enrollment.SKCList[skcIndex].SKUList {
			prices := enrollment.SKCList[skcIndex].SKUList[skuIndex].SitePriceList
			currentPrices := prices[:0]
			for _, price := range prices {
				if _, ok := activeSites[price.SiteID]; ok {
					currentPrices = append(currentPrices, price)
				}
			}
			enrollment.SKCList[skcIndex].SKUList[skuIndex].SitePriceList = currentPrices
		}
	}
	return enrollment, true
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
	copy.Enrollments = append([]temu.MarketingEnrollment(nil), source.Enrollments...)
	copy.GoodsBySKC = make(map[int64]temu.GoodsSummary, len(source.GoodsBySKC))
	for key, value := range source.GoodsBySKC {
		value.ProductSKUSummaries = append([]temu.GoodsSKUInfo(nil), value.ProductSKUSummaries...)
		copy.GoodsBySKC[key] = value
	}
	return copy
}

func (s Snapshot) String() string {
	return fmt.Sprintf("current_enrollments=%d pages=%d goods_skcs=%d goods_pages=%d", len(s.Enrollments), s.EnrollmentPages, len(s.GoodsBySKC), s.GoodsPages)
}
