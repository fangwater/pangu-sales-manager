package marketing

import (
	"sort"

	"pangu-sales-manager/internal/temu"
)

type ActivityRowFilter struct {
	SKCID        int64
	SKUID        int64
	SiteID       int64
	EnrollID     int64
	ActivityType int64
}

type ActivitySnapshotRow struct {
	EnrollID               int64  `json:"enroll_id"`
	ProductID              int64  `json:"product_id"`
	GoodsID                int64  `json:"goods_id"`
	ActivityType           int64  `json:"activity_type"`
	ActivityTypeName       string `json:"activity_type_name"`
	ActivityThematicID     int64  `json:"activity_thematic_id,omitempty"`
	ActivityThematicName   string `json:"activity_thematic_name,omitempty"`
	EnrollStatus           int    `json:"enroll_status"`
	SoldStatus             int    `json:"sold_status"`
	EnrollTime             int64  `json:"enroll_time"`
	ActivityStock          int64  `json:"activity_stock"`
	RemainingActivityStock int64  `json:"remaining_activity_stock"`
	ConsumedActivityStock  int64  `json:"consumed_activity_stock"`
	EnrollmentSKUCount     int    `json:"enrollment_sku_count"`
	SKCID                  int64  `json:"skc_id"`
	SKUID                  int64  `json:"sku_id"`
	Currency               string `json:"currency"`
	SiteID                 int64  `json:"site_id"`
	SiteName               string `json:"site_name"`
	SiteDailyPrice         int64  `json:"site_daily_price"`
	SiteActivityPrice      int64  `json:"site_activity_price"`
	SessionID              int64  `json:"session_id"`
	SessionName            string `json:"session_name"`
	SessionStatus          int    `json:"session_status"`
	SessionStartTime       int64  `json:"session_start_time"`
	SessionEndTime         int64  `json:"session_end_time"`
}

type ActivitySnapshotSummary struct {
	EnrollmentCount              int   `json:"enrollment_count"`
	ActiveEnrollmentCount        int   `json:"active_enrollment_count"`
	StockConsumedEnrollmentCount int   `json:"stock_consumed_enrollment_count"`
	TotalActivityStock           int64 `json:"total_activity_stock"`
	TotalRemainingActivityStock  int64 `json:"total_remaining_activity_stock"`
	EnrollmentPages              int   `json:"enrollment_pages"`
}

func (s *Syncer) ActivityRows(filter ActivityRowFilter) ([]ActivitySnapshotRow, ActivitySnapshotSummary, Snapshot, error) {
	snapshot := s.Snapshot()
	if !snapshot.Successful() {
		return nil, ActivitySnapshotSummary{}, snapshot, ErrSnapshotUnavailable
	}
	summary := summarizeSnapshot(snapshot)
	rows := make([]ActivitySnapshotRow, 0, len(snapshot.Enrollments))
	for _, enrollment := range snapshot.Enrollments {
		if filter.EnrollID != 0 && enrollment.EnrollID != filter.EnrollID {
			continue
		}
		if filter.ActivityType != 0 && enrollment.ActivityType != filter.ActivityType {
			continue
		}
		rows = append(rows, enrollmentRows(enrollment, filter)...)
	}
	sort.Slice(rows, func(left, right int) bool {
		if rows[left].SKCID != rows[right].SKCID {
			return rows[left].SKCID < rows[right].SKCID
		}
		if rows[left].SKUID != rows[right].SKUID {
			return rows[left].SKUID < rows[right].SKUID
		}
		if rows[left].ActivityType != rows[right].ActivityType {
			return rows[left].ActivityType < rows[right].ActivityType
		}
		if rows[left].EnrollID != rows[right].EnrollID {
			return rows[left].EnrollID < rows[right].EnrollID
		}
		if rows[left].SiteID != rows[right].SiteID {
			return rows[left].SiteID < rows[right].SiteID
		}
		return rows[left].SessionID < rows[right].SessionID
	})
	return rows, summary, snapshot, nil
}

func summarizeSnapshot(snapshot Snapshot) ActivitySnapshotSummary {
	summary := ActivitySnapshotSummary{
		EnrollmentCount: len(snapshot.Enrollments), EnrollmentPages: snapshot.EnrollmentPages,
	}
	for _, enrollment := range snapshot.Enrollments {
		summary.TotalActivityStock += enrollment.ActivityStock
		summary.TotalRemainingActivityStock += enrollment.RemainingActivityStock
		if enrollment.RemainingActivityStock < enrollment.ActivityStock {
			summary.StockConsumedEnrollmentCount++
		}
		for _, session := range enrollment.AssignedSessions {
			if session.SessionStatus == 2 {
				summary.ActiveEnrollmentCount++
				break
			}
		}
	}
	return summary
}

func enrollmentRows(enrollment temu.MarketingEnrollment, filter ActivityRowFilter) []ActivitySnapshotRow {
	base := ActivitySnapshotRow{
		EnrollID: enrollment.EnrollID, ProductID: enrollment.ProductID, GoodsID: enrollment.GoodsID,
		ActivityType: enrollment.ActivityType, ActivityTypeName: enrollment.ActivityTypeName,
		ActivityThematicID: enrollment.ActivityThematicID, ActivityThematicName: enrollment.ActivityThematicName,
		EnrollStatus: enrollment.EnrollStatus, SoldStatus: enrollment.SoldStatus, EnrollTime: enrollment.EnrollTime,
		ActivityStock: enrollment.ActivityStock, RemainingActivityStock: enrollment.RemainingActivityStock,
		ConsumedActivityStock: max(enrollment.ActivityStock-enrollment.RemainingActivityStock, 0),
		EnrollmentSKUCount:    enrollmentSKUCount(enrollment), Currency: enrollment.Currency,
	}
	if len(enrollment.SKCList) == 0 {
		return filteredRows(rowsForSessions(base, enrollment.AssignedSessions, nil), filter)
	}
	rows := make([]ActivitySnapshotRow, 0)
	for _, skc := range enrollment.SKCList {
		if filter.SKCID != 0 && skc.SKCID != filter.SKCID {
			continue
		}
		skcRow := base
		skcRow.SKCID = skc.SKCID
		skcRow.Currency = firstNonEmpty(skc.Currency, base.Currency)
		if len(skc.SKUList) == 0 {
			rows = append(rows, rowsForSessions(skcRow, enrollment.AssignedSessions, nil)...)
			continue
		}
		for _, sku := range skc.SKUList {
			if filter.SKUID != 0 && sku.SKUID != filter.SKUID {
				continue
			}
			skuRow := skcRow
			skuRow.SKUID = sku.SKUID
			skuRow.Currency = firstNonEmpty(sku.Currency, skcRow.Currency)
			rows = append(rows, rowsForSessions(skuRow, enrollment.AssignedSessions, sku.SitePriceList)...)
		}
	}
	return filteredRows(rows, filter)
}

func rowsForSessions(base ActivitySnapshotRow, sessions []temu.MarketingEnrollmentSession, prices []temu.MarketingEnrollmentPrice) []ActivitySnapshotRow {
	rows := make([]ActivitySnapshotRow, 0, max(len(sessions), len(prices)))
	matchedPrices := make([]bool, len(prices))
	for _, session := range sessions {
		matched := false
		for index, price := range prices {
			if session.SiteID != 0 && price.SiteID != 0 && session.SiteID != price.SiteID {
				continue
			}
			rows = append(rows, activityRowWithSessionAndPrice(base, session, price))
			matchedPrices[index] = true
			matched = true
		}
		if !matched {
			rows = append(rows, activityRowWithSessionAndPrice(base, session, temu.MarketingEnrollmentPrice{}))
		}
	}
	for index, price := range prices {
		if !matchedPrices[index] {
			rows = append(rows, activityRowWithSessionAndPrice(base, temu.MarketingEnrollmentSession{}, price))
		}
	}
	if len(rows) == 0 {
		rows = append(rows, base)
	}
	return rows
}

func activityRowWithSessionAndPrice(base ActivitySnapshotRow, session temu.MarketingEnrollmentSession, price temu.MarketingEnrollmentPrice) ActivitySnapshotRow {
	row := base
	row.SessionID = session.SessionID
	row.SessionName = session.SessionName
	row.SessionStatus = session.SessionStatus
	row.SessionStartTime = session.StartTime
	row.SessionEndTime = session.EndTime
	row.SiteID = price.SiteID
	row.SiteName = price.SiteName
	if row.SiteID == 0 {
		row.SiteID = session.SiteID
	}
	if row.SiteName == "" {
		row.SiteName = session.SiteName
	}
	row.SiteDailyPrice = price.DailyPrice
	row.SiteActivityPrice = price.ActivityPrice
	return row
}

func filteredRows(rows []ActivitySnapshotRow, filter ActivityRowFilter) []ActivitySnapshotRow {
	filtered := rows[:0]
	for _, row := range rows {
		if filter.SKCID != 0 && row.SKCID != filter.SKCID {
			continue
		}
		if filter.SKUID != 0 && row.SKUID != filter.SKUID {
			continue
		}
		if filter.SiteID != 0 && row.SiteID != filter.SiteID {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func enrollmentSKUCount(enrollment temu.MarketingEnrollment) int {
	count := 0
	for _, skc := range enrollment.SKCList {
		count += len(skc.SKUList)
	}
	return count
}
