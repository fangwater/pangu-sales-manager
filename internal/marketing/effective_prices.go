package marketing

import (
	"errors"
	"sort"

	"pangu-sales-manager/internal/temu"
)

var ErrSnapshotUnavailable = errors.New("marketing activity snapshot is not available")

type EffectivePriceFilter struct {
	SKCID  int64
	SKUID  int64
	SiteID int64
}

type EffectiveActivityPrice struct {
	ProductID              int64            `json:"product_id"`
	SKCID                  int64            `json:"skc_id"`
	SKUID                  int64            `json:"sku_id"`
	SiteID                 int64            `json:"site_id"`
	SiteName               string           `json:"site_name"`
	Currency               string           `json:"currency"`
	EffectiveActivityPrice int64            `json:"effective_activity_price"`
	ActiveActivities       []ActiveActivity `json:"active_activities"`
	WinningActivities      []ActiveActivity `json:"winning_activities"`
}

type ActiveActivity struct {
	EnrollID               int64  `json:"enroll_id"`
	ActivityType           int64  `json:"activity_type"`
	ActivityTypeName       string `json:"activity_type_name"`
	ActivityThematicID     int64  `json:"activity_thematic_id,omitempty"`
	ActivityThematicName   string `json:"activity_thematic_name,omitempty"`
	SessionID              int64  `json:"session_id"`
	SessionName            string `json:"session_name"`
	SessionStatus          int    `json:"session_status"`
	SessionStartTime       int64  `json:"session_start_time"`
	SessionEndTime         int64  `json:"session_end_time"`
	DailyPrice             int64  `json:"daily_price"`
	ActivityPrice          int64  `json:"activity_price"`
	ActivityStock          int64  `json:"activity_stock"`
	RemainingActivityStock int64  `json:"remaining_activity_stock"`
	EnrollStatus           int    `json:"enroll_status"`
}

type effectivePriceKey struct {
	productID int64
	skcID     int64
	skuID     int64
	siteID    int64
}

func (s *Syncer) EffectivePrices(filter EffectivePriceFilter) ([]EffectiveActivityPrice, Snapshot, error) {
	snapshot := s.Snapshot()
	if !snapshot.Successful() {
		return nil, snapshot, ErrSnapshotUnavailable
	}
	prices := make(map[effectivePriceKey]*EffectiveActivityPrice)
	for _, enrollment := range snapshot.Enrollments {
		for _, session := range enrollment.AssignedSessions {
			if session.SessionStatus != 2 {
				continue
			}
			for _, skc := range enrollment.SKCList {
				if filter.SKCID != 0 && skc.SKCID != filter.SKCID {
					continue
				}
				for _, sku := range skc.SKUList {
					if filter.SKUID != 0 && sku.SKUID != filter.SKUID {
						continue
					}
					for _, sitePrice := range sku.SitePriceList {
						if sitePrice.SiteID != session.SiteID || (filter.SiteID != 0 && sitePrice.SiteID != filter.SiteID) {
							continue
						}
						key := effectivePriceKey{productID: enrollment.ProductID, skcID: skc.SKCID, skuID: sku.SKUID, siteID: sitePrice.SiteID}
						item := prices[key]
						if item == nil {
							item = &EffectiveActivityPrice{
								ProductID: enrollment.ProductID, SKCID: skc.SKCID, SKUID: sku.SKUID,
								SiteID: sitePrice.SiteID, SiteName: sitePrice.SiteName,
								Currency:               firstNonEmpty(sku.Currency, skc.Currency, enrollment.Currency),
								EffectiveActivityPrice: sitePrice.ActivityPrice,
							}
							prices[key] = item
						}
						activity := activeActivity(enrollment, session, sitePrice)
						item.ActiveActivities = append(item.ActiveActivities, activity)
						if activity.ActivityPrice < item.EffectiveActivityPrice {
							item.EffectiveActivityPrice = activity.ActivityPrice
						}
					}
				}
			}
		}
	}

	items := make([]EffectiveActivityPrice, 0, len(prices))
	for _, item := range prices {
		sort.Slice(item.ActiveActivities, func(left, right int) bool {
			return compareActivities(item.ActiveActivities[left], item.ActiveActivities[right])
		})
		for _, activity := range item.ActiveActivities {
			if activity.ActivityPrice == item.EffectiveActivityPrice {
				item.WinningActivities = append(item.WinningActivities, activity)
			}
		}
		items = append(items, *item)
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].SKCID != items[right].SKCID {
			return items[left].SKCID < items[right].SKCID
		}
		if items[left].SKUID != items[right].SKUID {
			return items[left].SKUID < items[right].SKUID
		}
		return items[left].SiteID < items[right].SiteID
	})
	return items, snapshot, nil
}

func activeActivity(enrollment temu.MarketingEnrollment, session temu.MarketingEnrollmentSession, sitePrice temu.MarketingEnrollmentPrice) ActiveActivity {
	return ActiveActivity{
		EnrollID: enrollment.EnrollID, ActivityType: enrollment.ActivityType,
		ActivityTypeName: enrollment.ActivityTypeName, ActivityThematicID: enrollment.ActivityThematicID,
		ActivityThematicName: enrollment.ActivityThematicName, SessionID: session.SessionID,
		SessionName: session.SessionName, SessionStatus: session.SessionStatus,
		SessionStartTime: session.StartTime, SessionEndTime: session.EndTime,
		DailyPrice: sitePrice.DailyPrice, ActivityPrice: sitePrice.ActivityPrice,
		ActivityStock: enrollment.ActivityStock, RemainingActivityStock: enrollment.RemainingActivityStock,
		EnrollStatus: enrollment.EnrollStatus,
	}
}

func compareActivities(left, right ActiveActivity) bool {
	if left.ActivityPrice != right.ActivityPrice {
		return left.ActivityPrice < right.ActivityPrice
	}
	if left.ActivityType != right.ActivityType {
		return left.ActivityType < right.ActivityType
	}
	if left.SessionID != right.SessionID {
		return left.SessionID < right.SessionID
	}
	return left.EnrollID < right.EnrollID
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
