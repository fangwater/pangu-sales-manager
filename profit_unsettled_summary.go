package main

import (
	"context"
	"sort"
)

// ProfitUnsettledShopSummary is a per-shop rollup of "待处理款项" (PO amounts
// not yet settled) and "发货面单费-待出账" (shipping label fees not yet
// posted). Both source tables have no date column at all, so this is a
// standing estimate, not a bucketed history. Reconciliation deducts the posted
// components, retains partial balances, and keeps unresolved source semantics visible.
type ProfitUnsettledShopSummary struct {
	ShopKey                     string  `json:"shop_key"`
	Currency                    string  `json:"currency"`
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
	Currency      string  `json:"currency"`
	SKUID         int64   `json:"sku_id"`
	SKUName       string  `json:"sku_name"`
	Quantity      float64 `json:"quantity"`
	DeclaredTotal float64 `json:"declared_total"`
	PartialOrders int64   `json:"partial_orders"`
}

type profitCurrencyShop struct{ ShopKey, Currency string }

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
	byShop := make(map[profitCurrencyShop]int, len(shops))
	result := make([]ProfitUnsettledShopSummary, len(shops))
	for i, shop := range shops {
		result[i] = shop
		byShop[profitCurrencyShop{shop.ShopKey, shop.Currency}] = i
	}
	for key, amount := range pendingShippingFees {
		index, ok := byShop[key]
		if !ok {
			index = len(result)
			result = append(result, ProfitUnsettledShopSummary{ShopKey: key.ShopKey, Currency: key.Currency})
			byShop[key] = index
		}
		result[index].PendingShippingLabelFee = amount
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ShopKey < result[j].ShopKey || result[i].ShopKey == result[j].ShopKey && result[i].Currency < result[j].Currency
	})

	skus, err := s.queryProfitUnsettledSKUs(ctx, shopKey)
	if err != nil {
		return ProfitUnsettledSummaryResponse{}, err
	}

	return ProfitUnsettledSummaryResponse{Shops: result, SKUs: skus}, nil
}

func (s *Store) queryProfitUnsettledShops(ctx context.Context, shopKey string) ([]ProfitUnsettledShopSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT shop_key,currency,
		 COALESCE(SUM(remaining_sales_receipt) FILTER(WHERE status<>'settled'),0),
		 COALESCE(SUM(sales_receipt_after_discount) FILTER(WHERE status<>'settled'),0),
		 COALESCE(SUM(remaining_sales_chargeback) FILTER(WHERE status<>'settled'),0),
		 COALESCE(SUM(remaining_freight_receipt) FILTER(WHERE status<>'settled'),0),
		 COALESCE(SUM(freight_receipt_after_discount) FILTER(WHERE status<>'settled'),0),
		 COALESCE(SUM(remaining_freight_chargeback) FILTER(WHERE status<>'settled'),0)
		FROM profit_pending_order_reconciliation
		WHERE snapshot_active AND ($1='' OR shop_key=$1)
		GROUP BY shop_key,currency ORDER BY shop_key,currency
	`, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ProfitUnsettledShopSummary
	for rows.Next() {
		var row ProfitUnsettledShopSummary
		if err := rows.Scan(&row.ShopKey, &row.Currency, &row.SalesReceipt, &row.SalesReceiptAfterDiscount, &row.SalesChargeback,
			&row.FreightReceipt, &row.FreightReceiptAfterDiscount, &row.FreightChargeback); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryProfitPendingShippingLabelFees(ctx context.Context, shopKey string) (map[profitCurrencyShop]float64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT shop_key,currency,COALESCE(SUM(remaining_amount) FILTER(WHERE status<>'settled'),0)
		FROM profit_pending_shipping_reconciliation
		WHERE snapshot_active AND ($1='' OR shop_key=$1)
		GROUP BY shop_key,currency
	`, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[profitCurrencyShop]float64)
	for rows.Next() {
		var key profitCurrencyShop
		var amount float64
		if err := rows.Scan(&key.ShopKey, &key.Currency, &amount); err != nil {
			return nil, err
		}
		result[key] = amount
	}
	return result, rows.Err()
}

func (s *Store) queryProfitUnsettledSKUs(ctx context.Context, shopKey string) ([]ProfitUnsettledSKURow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.shop_key,o.currency,sk.sku_id,MAX(sk.sku_name),
		       COALESCE(SUM(sk.quantity),0),COALESCE(SUM(sk.quantity*sk.declared_price),0),
		       COUNT(DISTINCT o.id) FILTER(WHERE o.status='partial')
		FROM temu_profit_unsettle_skus sk
		JOIN profit_pending_order_reconciliation o ON o.id=sk.unsettle_order_id
		WHERE o.status IN ('pending','partial','review') AND ($1='' OR o.shop_key=$1)
		GROUP BY o.shop_key,o.currency,sk.sku_id ORDER BY o.shop_key,o.currency,sk.sku_id
	`, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ProfitUnsettledSKURow
	for rows.Next() {
		var row ProfitUnsettledSKURow
		if err := rows.Scan(&row.ShopKey, &row.Currency, &row.SKUID, &row.SKUName, &row.Quantity, &row.DeclaredTotal, &row.PartialOrders); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
