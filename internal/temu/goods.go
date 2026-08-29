package temu

import "context"

const (
	GoodsListAPI         = "bg.glo.goods.list.get"
	SKCSiteStatusOnShelf = 1
)

type GoodsListResult struct {
	TotalCount int            `json:"totalCount"`
	Data       []GoodsSummary `json:"data"`
}

type GoodsSummary struct {
	ProductID           int64          `json:"productId"`
	ProductSKCID        int64          `json:"productSkcId"`
	SKCSiteStatus       int            `json:"skcSiteStatus"`
	ProductSKUSummaries []GoodsSKUInfo `json:"productSkuSummaries"`
}

type GoodsSKUInfo struct {
	ProductSKUID int64 `json:"productSkuId"`
}

func (c *Client) GoodsPage(ctx context.Context, productSKCIDs []int64, page, pageSize int) (GoodsListResult, error) {
	var result GoodsListResult
	_, err := c.Call(ctx, GoodsListAPI, map[string]any{
		"productSkcIds": productSKCIDs, "page": page, "pageSize": pageSize,
	}, &result)
	return result, err
}
