package main

import "context"

// ProfitUnsettledShopSummary is a per-shop rollup of "待处理款项" (PO amounts
// not yet settled) and "发货面单费-待出账" (shipping label fees not yet
// posted). Both source tables have no date column at all, so this is a
// standing estimate, not a bucketed history — once the real posted data
// lands in temu_profit_settled_flows / shipping_label_fees(billing_phase=
// 'posted'), it naturally supersedes this estimate on the daily summary page.
type ProfitUnsettledShopSummary struct {
	ShopKey                     string  `json:"shop_key"`
	SalesReceipt                float64 `json:"sales_receipt"`
	SalesReceiptAfterDiscount   float64 `json:"sales_receipt_after_discount"`
	SalesChargeback             float64 `json:"sales_chargeback"`
	FreightReceipt              float64 `json:"freight_receipt"`
	FreightReceiptAfterDiscount float64 `json:"freight_receipt_after_discount"`
	FreightChargeback           float64 `json:"freight_chargeback"`
	PendingShippingLabelFee     float64 `json:"pending_shipping_label_fee"`
}

// ProfitUnsettledSKURow is a pending-PO SKU rollup. sku_id here is TEMU's
// internal numeric SKU id, not sku_ext_code/platform_sku — the unsettle
// tables never recorded the seller SKU code, so this cannot be joined against
// the platform-SKU-keyed profit-sku-summary view.
type ProfitUnsettledSKURow struct {
	ShopKey       string  `json:"shop_key"`
	SKUID         int64   `json:"sku_id"`
	SKUName       string  `json:"sku_name"`
	Quantity      float64 `json:"quantity"`
	DeclaredTotal float64 `json:"declared_total"`
}

type ProfitUnsettledSummaryResponse struct {
	Shops []ProfitUnsettledShopSummary `json:"shops"`
	SKUs  []ProfitUnsettledSKURow      `json:"skus"`
}

func (s *Store) profitUnsettledSummary(ctx context.Context, shopKey string) (ProfitUnsettledSummaryResponse, error) {
	shops, err := s.queryProfitUnsettledShops(ctx, shopKey)
	if err != nil {
		return ProfitUnsettledSummaryResponse{}, err
	}
	pendingShippingFees, err := s.queryProfitPendingShippingLabelFees(ctx, shopKey)
	if err != nil {
		return ProfitUnsettledSummaryResponse{}, err
	}
	byShop := make(map[string]*ProfitUnsettledShopSummary, len(shops))
	result := make([]ProfitUnsettledShopSummary, len(shops))
	for i, shop := range shops {
		result[i] = shop
		byShop[shop.ShopKey] = &result[i]
	}
	for shopKey, amount := range pendingShippingFees {
		row, ok := byShop[shopKey]
		if !ok {
			result = append(result, ProfitUnsettledShopSummary{ShopKey: shopKey})
			row = &result[len(result)-1]
			byShop[shopKey] = row
		}
		row.PendingShippingLabelFee = amount
	}

	skus, err := s.queryProfitUnsettledSKUs(ctx, shopKey)
	if err != nil {
		return ProfitUnsettledSummaryResponse{}, err
	}

	return ProfitUnsettledSummaryResponse{Shops: result, SKUs: skus}, nil
}

func (s *Store) queryProfitUnsettledShops(ctx context.Context, shopKey string) ([]ProfitUnsettledShopSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT shop_key,
		       COALESCE(SUM(sales_receipt), 0), COALESCE(SUM(sales_receipt_after_discount), 0), COALESCE(SUM(sales_chargeback), 0),
		       COALESCE(SUM(freight_receipt), 0), COALESCE(SUM(freight_receipt_after_discount), 0), COALESCE(SUM(freight_chargeback), 0)
		FROM temu_profit_unsettle_orders
		WHERE ($1='' OR shop_key=$1)
		GROUP BY shop_key ORDER BY shop_key
	`, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ProfitUnsettledShopSummary
	for rows.Next() {
		var row ProfitUnsettledShopSummary
		if err := rows.Scan(&row.ShopKey, &row.SalesReceipt, &row.SalesReceiptAfterDiscount, &row.SalesChargeback,
			&row.FreightReceipt, &row.FreightReceiptAfterDiscount, &row.FreightChargeback); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryProfitPendingShippingLabelFees(ctx context.Context, shopKey string) (map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT shop_key, COALESCE(SUM(freight_amount), 0)
		FROM temu_profit_shipping_label_fees
		WHERE billing_phase='pending' AND ($1='' OR shop_key=$1)
		GROUP BY shop_key
	`, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]float64)
	for rows.Next() {
		var shop string
		var amount float64
		if err := rows.Scan(&shop, &amount); err != nil {
			return nil, err
		}
		result[shop] = amount
	}
	return result, rows.Err()
}

func (s *Store) queryProfitUnsettledSKUs(ctx context.Context, shopKey string) ([]ProfitUnsettledSKURow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.shop_key, sk.sku_id, MAX(sk.sku_name),
		       COALESCE(SUM(sk.quantity), 0), COALESCE(SUM(sk.quantity * sk.declared_price), 0)
		FROM temu_profit_unsettle_skus sk
		JOIN temu_profit_unsettle_orders o ON o.id=sk.unsettle_order_id
		WHERE ($1='' OR o.shop_key=$1)
		GROUP BY o.shop_key, sk.sku_id
		ORDER BY o.shop_key, sk.sku_id
	`, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ProfitUnsettledSKURow
	for rows.Next() {
		var row ProfitUnsettledSKURow
		if err := rows.Scan(&row.ShopKey, &row.SKUID, &row.SKUName, &row.Quantity, &row.DeclaredTotal); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
