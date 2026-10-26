package main

import (
	"context"
	"sort"
	"time"
)

// ProfitSKUSummaryRow includes explicit SKU settlements and uniquely resolved
// shop/PO or SKU-ID matches. Unresolved flows remain in Attribution totals.
type ProfitSKUSummaryRow struct {
	PlatformSKU string `json:"platform_sku"`
	SKUName     string `json:"sku_name"`

	Units        float64 `json:"units"`
	RefundOrders int64   `json:"refund_orders"`

	SalesChargebacks   int64 `json:"sales_chargebacks"`
	FreightChargebacks int64 `json:"freight_chargebacks"`

	SalesReceiptAmount      float64 `json:"sales_receipt_amount"`
	FreightReceiptAmount    float64 `json:"freight_receipt_amount"`
	SalesChargebackAmount   float64 `json:"sales_chargeback_amount"`
	FreightChargebackAmount float64 `json:"freight_chargeback_amount"`
	PaybackAmount           float64 `json:"payback_amount"`
	SettledRows             int64   `json:"settled_rows"`
	InferredSettledRows     int64   `json:"inferred_settled_rows"`

	EstimatedSalesAmount  float64 `json:"estimated_sales_amount"`
	EstimatedSalesMatched int64   `json:"estimated_sales_matched"`
	EstimatedSalesTotal   int64   `json:"estimated_sales_total"`
}

type ProfitSKUSummaryResponse struct {
	Currency    string                   `json:"currency"`
	Range       DateRange                `json:"range"`
	Rows        []ProfitSKUSummaryRow    `json:"rows"`
	Attribution []ProfitAttributionTotal `json:"attribution"`
}

type profitSKUUnit struct {
	PlatformSKU string
	SKUName     string
	Units       float64
	Matched     int64
	Total       int64
	Amount      float64
}

type profitSKUCount struct {
	PlatformSKU string
	Count       int64
}

type profitSKUSettled struct {
	SKUExtCode              string
	SKUName                 string
	SalesChargebacks        int64
	FreightChargebacks      int64
	PaybackAmount           float64
	SalesReceiptAmount      float64
	FreightReceiptAmount    float64
	SalesChargebackAmount   float64
	FreightChargebackAmount float64
	SettledRows             int64
	InferredSettledRows     int64
}

// profitSKUSummary rolls TEMU sales/refund/settlement data up by platform SKU
// over [start, end). Reuses the same join keys as profitDailySummary: TEMU's
// sku_ext_code (settled_flows) lines up exactly with normalized_order_lines'
// platform_sku, verified against real data.
func (s *Store) profitSKUSummary(ctx context.Context, timezone string, start, end time.Time, shopKey string) (ProfitSKUSummaryResponse, error) {
	return s.profitSKUSummaryCurrency(ctx, timezone, start, end, shopKey, "USD")
}

func (s *Store) profitSKUSummaryCurrency(ctx context.Context, timezone string, start, end time.Time, shopKey, currency string) (ProfitSKUSummaryResponse, error) {
	units, err := s.queryProfitSKUUnits(ctx, start, end, shopKey, currency)
	if err != nil {
		return ProfitSKUSummaryResponse{}, err
	}
	refundOrders, err := s.queryProfitSKUOrderRefunds(ctx, start, end, shopKey)
	if err != nil {
		return ProfitSKUSummaryResponse{}, err
	}
	settled, err := s.queryProfitSKUSettledFlow(ctx, start, end, shopKey, currency)
	if err != nil {
		return ProfitSKUSummaryResponse{}, err
	}

	rowsBySKU := make(map[string]*ProfitSKUSummaryRow)
	rowFor := func(platformSKU string) *ProfitSKUSummaryRow {
		row, ok := rowsBySKU[platformSKU]
		if !ok {
			row = &ProfitSKUSummaryRow{PlatformSKU: platformSKU}
			rowsBySKU[platformSKU] = row
		}
		return row
	}

	for _, u := range units {
		row := rowFor(u.PlatformSKU)
		row.Units += u.Units
		row.EstimatedSalesAmount += u.Amount
		row.EstimatedSalesMatched += u.Matched
		row.EstimatedSalesTotal += u.Total
		if row.SKUName == "" {
			row.SKUName = u.SKUName
		}
	}
	for _, c := range refundOrders {
		rowFor(c.PlatformSKU).RefundOrders += c.Count
	}
	for _, sf := range settled {
		row := rowFor(sf.SKUExtCode)
		row.SalesChargebacks += sf.SalesChargebacks
		row.FreightChargebacks += sf.FreightChargebacks
		row.PaybackAmount += sf.PaybackAmount
		row.SettledRows += sf.SettledRows
		row.InferredSettledRows += sf.InferredSettledRows
		row.SalesReceiptAmount += sf.SalesReceiptAmount
		row.FreightReceiptAmount += sf.FreightReceiptAmount
		row.SalesChargebackAmount += sf.SalesChargebackAmount
		row.FreightChargebackAmount += sf.FreightChargebackAmount
		if row.SKUName == "" {
			row.SKUName = sf.SKUName
		}
	}

	rows := make([]ProfitSKUSummaryRow, 0, len(rowsBySKU))
	for _, row := range rowsBySKU {
		rows = append(rows, *row)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].PlatformSKU < rows[j].PlatformSKU })
	attribution, err := s.queryProfitAttributionTotals(ctx, start, end, shopKey)
	if err != nil {
		return ProfitSKUSummaryResponse{}, err
	}
	return ProfitSKUSummaryResponse{
		Attribution: attribution,
		Currency:    currency,
		Range:       DateRange{Start: start.Format("2006-01-02"), End: end.AddDate(0, 0, -1).Format("2006-01-02")},
		Rows:        rows,
	}, nil
}

func (s *Store) queryProfitSKUUnits(ctx context.Context, start, end time.Time, shopKey, currency string) ([]profitSKUUnit, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.platform_sku, MAX(COALESCE(NULLIF(l.product_name,''), sku.product_name)),
		       COALESCE(SUM(l.quantity), 0),
		       COUNT(*) FILTER (WHERE l.unit_price IS NOT NULL AND l.currency=$4),
		       COUNT(*),
		       COALESCE(SUM(l.quantity * l.unit_price) FILTER (WHERE l.unit_price IS NOT NULL AND l.currency=$4), 0)
		FROM normalized_orders o
		JOIN normalized_order_lines l ON l.order_id=o.id
		LEFT JOIN canonical_skus sku ON sku.warehouse_sku=l.warehouse_sku
		WHERE o.platform='temu' AND o.sales_eligible
		  AND o.occurred_at >= $1 AND o.occurred_at < $2
		  AND ($3='' OR o.shop_key=$3)
		GROUP BY 1
	`, start, end, shopKey, currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitSKUUnit
	for rows.Next() {
		var row profitSKUUnit
		if err := rows.Scan(&row.PlatformSKU, &row.SKUName, &row.Units, &row.Matched, &row.Total, &row.Amount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryProfitSKUOrderRefunds(ctx context.Context, start, end time.Time, shopKey string) ([]profitSKUCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.platform_sku, COUNT(*)
		FROM normalized_orders o
		JOIN normalized_order_lines l ON l.order_id=o.id
		WHERE o.platform='temu' AND NOT o.sales_eligible
		  AND o.occurred_at >= $1 AND o.occurred_at < $2
		  AND ($3='' OR o.shop_key=$3)
		GROUP BY 1
	`, start, end, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitSKUCount
	for rows.Next() {
		var row profitSKUCount
		if err := rows.Scan(&row.PlatformSKU, &row.Count); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryProfitSKUSettledFlow(ctx context.Context, start, end time.Time, shopKey, currency string) ([]profitSKUSettled, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.platform_sku, MAX(f.sku_name),
		       COUNT(*) FILTER (WHERE f.trade_type='销售冲回'),
		       COUNT(*) FILTER (WHERE f.trade_type='运费冲回'),
		       COALESCE(SUM(f.settle_amount), 0),
		       COALESCE(SUM(f.settle_amount) FILTER (WHERE f.trade_type='销售回款'), 0),
		       COALESCE(SUM(f.settle_amount) FILTER (WHERE f.trade_type='运费回款'), 0),
		       COALESCE(SUM(f.settle_amount) FILTER (WHERE f.trade_type='销售冲回'), 0),
		       COALESCE(SUM(f.settle_amount) FILTER (WHERE f.trade_type='运费冲回'), 0), COUNT(*),COUNT(*) FILTER(WHERE f.attribution_method<>'direct')
		FROM profit_settlement_attribution f
		WHERE f.platform_sku <> '' AND f.received_at IS NOT NULL
		  AND f.received_at >= $1 AND f.received_at < $2
		  AND ($3='' OR f.shop_key=$3) AND f.currency=$4
		GROUP BY 1
	`, start, end, shopKey, currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitSKUSettled
	for rows.Next() {
		var row profitSKUSettled
		if err := rows.Scan(&row.SKUExtCode, &row.SKUName, &row.SalesChargebacks, &row.FreightChargebacks, &row.PaybackAmount,
			&row.SalesReceiptAmount, &row.FreightReceiptAmount, &row.SalesChargebackAmount, &row.FreightChargebackAmount, &row.SettledRows, &row.InferredSettledRows); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
