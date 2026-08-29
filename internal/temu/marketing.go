package temu

import (
	"context"
	"encoding/json"
)

const (
	MarketingActivityListAPI   = "bg.marketing.activity.list.get.global"
	MarketingActivityDetailAPI = "bg.marketing.activity.detail.get.global"
	MarketingEnrollmentListAPI = "bg.marketing.activity.enroll.list.get.global"
)

type MarketingActivityListResult struct {
	ActivityList []MarketingActivity `json:"activityList"`
}

type MarketingActivity struct {
	ActivityType        int64           `json:"activityType"`
	ActivityName        string          `json:"activityName"`
	ActivityCopywriting string          `json:"activityCopywriting"`
	ActivityTagList     json.RawMessage `json:"activityTagList"`
	ThematicList        json.RawMessage `json:"activityThematicList"`
}

type MarketingActivityDetail struct {
	Requirements json.RawMessage `json:"requirements"`
	MallAptitude json.RawMessage `json:"mallAptitude"`
	ThematicInfo json.RawMessage `json:"thematicInfo"`
	ActivityInfo json.RawMessage `json:"activityInfo"`
	CanEnroll    *bool           `json:"canEnroll"`
}

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
	SKCID         int64                    `json:"skcId"`
	Currency      string                   `json:"currency"`
	DailyPrice    int64                    `json:"dailyPrice"`
	ActivityPrice int64                    `json:"activityPrice"`
	SKUList       []MarketingEnrollmentSKU `json:"skuList"`
}

type MarketingEnrollmentSKU struct {
	SKUID         int64                      `json:"skuId"`
	Currency      string                     `json:"currency"`
	DailyPrice    int64                      `json:"dailyPrice"`
	ActivityPrice int64                      `json:"activityPrice"`
	SitePriceList []MarketingEnrollmentPrice `json:"sitePriceList"`
}

type MarketingEnrollmentPrice struct {
	SiteID        int64  `json:"siteId"`
	SiteName      string `json:"siteName"`
	DailyPrice    int64  `json:"dailyPrice"`
	ActivityPrice int64  `json:"activityPrice"`
}

func (c *Client) MarketingActivities(ctx context.Context) (MarketingActivityListResult, error) {
	var result MarketingActivityListResult
	_, err := c.Call(ctx, MarketingActivityListAPI, nil, &result)
	return result, err
}

func (c *Client) MarketingActivityDetail(ctx context.Context, activityType int64) (MarketingActivityDetail, error) {
	var result MarketingActivityDetail
	_, err := c.Call(ctx, MarketingActivityDetailAPI, map[string]any{"activityType": activityType}, &result)
	return result, err
}

func (c *Client) MarketingEnrollmentPage(ctx context.Context, pageNo, pageSize int) (MarketingEnrollmentListResult, error) {
	var result MarketingEnrollmentListResult
	_, err := c.Call(ctx, MarketingEnrollmentListAPI, map[string]any{
		"mallId": 0, "pageNo": pageNo, "pageSize": pageSize,
	}, &result)
	return result, err
}
