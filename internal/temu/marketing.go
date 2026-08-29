package temu

import (
	"context"
)

const (
	MarketingEnrollmentListAPI = "bg.marketing.activity.enroll.list.get.global"
	CurrentSessionStatus       = 2
)

type MarketingEnrollmentListResult struct {
	Total int                   `json:"total"`
	List  []MarketingEnrollment `json:"list"`
}

type MarketingEnrollment struct {
	EnrollID               int64                        `json:"enrollId"`
	ProductID              int64                        `json:"productId"`
	GoodsID                int64                        `json:"goodsId"`
	ActivityType           int64                        `json:"activityType"`
	ActivityTypeName       string                       `json:"activityTypeName"`
	ActivityThematicID     int64                        `json:"activityThematicId"`
	ActivityThematicName   string                       `json:"activityThematicName"`
	Currency               string                       `json:"currency"`
	ActivityStock          int64                        `json:"activityStock"`
	RemainingActivityStock int64                        `json:"remainingActivityStock"`
	EnrollStatus           int                          `json:"enrollStatus"`
	SoldStatus             int                          `json:"soldStatus"`
	EnrollTime             int64                        `json:"enrollTime"`
	SessionStartTime       int64                        `json:"sessionStartTime"`
	SessionEndTime         int64                        `json:"sessionEndTime"`
	AssignedSessions       []MarketingEnrollmentSession `json:"assignSessionList"`
	SKCList                []MarketingEnrollmentSKC     `json:"skcList"`
}

type MarketingEnrollmentSession struct {
	SessionID     int64  `json:"sessionId"`
	SessionName   string `json:"sessionName"`
	SessionStatus int    `json:"sessionStatus"`
	SiteID        int64  `json:"siteId"`
	SiteName      string `json:"siteName"`
	StartTime     int64  `json:"startTime"`
	EndTime       int64  `json:"endTime"`
	StartDate     string `json:"startDateStr"`
	EndDate       string `json:"endDateStr"`
	DurationDays  int    `json:"durationDays"`
}

type MarketingEnrollmentSKC struct {
	SKCID    int64                    `json:"skcId"`
	Currency string                   `json:"currency"`
	SKUList  []MarketingEnrollmentSKU `json:"skuList"`
}

type MarketingEnrollmentSKU struct {
	SKUID         int64                      `json:"skuId"`
	Currency      string                     `json:"currency"`
	SitePriceList []MarketingEnrollmentPrice `json:"sitePriceList"`
}

type MarketingEnrollmentPrice struct {
	SiteID        int64  `json:"siteId"`
	SiteName      string `json:"siteName"`
	DailyPrice    int64  `json:"dailyPrice"`
	ActivityPrice int64  `json:"activityPrice"`
}

func (c *Client) CurrentMarketingEnrollmentPage(ctx context.Context, pageNo, pageSize int) (MarketingEnrollmentListResult, error) {
	var result MarketingEnrollmentListResult
	_, err := c.Call(ctx, MarketingEnrollmentListAPI, map[string]any{
		"mallId": 0, "pageNo": pageNo, "pageSize": pageSize, "sessionStatus": CurrentSessionStatus,
	}, &result)
	return result, err
}
