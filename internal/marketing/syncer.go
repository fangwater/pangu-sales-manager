package marketing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"pangu-sales-manager/internal/temu"
)

const (
	enrollmentPageSize = 50
	maxRetryAttempts   = 3
)

// Snapshot is the latest complete activity pull. It is intentionally kept in
// process memory and rebuilt after every service restart.
type Snapshot struct {
	StartedAt       time.Time                  `json:"started_at"`
	CompletedAt     time.Time                  `json:"completed_at"`
	LastError       string                     `json:"last_error,omitempty"`
	Enrollments     []temu.MarketingEnrollment `json:"enrollments"`
	EnrollmentPages int                        `json:"enrollment_pages"`
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

	snapshot := Snapshot{StartedAt: s.now().UTC()}
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
	return copy
}

func (s Snapshot) String() string {
	return fmt.Sprintf("current_enrollments=%d pages=%d", len(s.Enrollments), s.EnrollmentPages)
}
