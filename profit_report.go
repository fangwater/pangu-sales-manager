package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Read-only provenance for the report. A missing import is different from a
// zero balance; expose source dates and currencies instead of assuming either.
type ProfitReportSource struct {
	ShopKey    string     `json:"shop_key"`
	Source     string     `json:"source"`
	Currency   string     `json:"currency"`
	Rows       int64      `json:"rows"`
	FirstAt    *time.Time `json:"first_at"`
	LastAt     *time.Time `json:"last_at"`
	ImportedAt *time.Time `json:"imported_at"`
}

type ProfitPriceCoverage struct {
	ShopKey        string     `json:"shop_key"`
	Lines          int64      `json:"lines"`
	PricedLines    int64      `json:"priced_lines"`
	ExactLines     int64      `json:"exact_lines"`
	InferredLines  int64      `json:"inferred_lines"`
	WarningLines   int64      `json:"warning_lines"`
	FirstSeenLines int64      `json:"first_seen_lines"`
	LatestBackfill *time.Time `json:"latest_backfill"`
}

type ProfitPendingCheck struct {
	ShopKey             string  `json:"shop_key"`
	Currency            string  `json:"currency"`
	Orders              int64   `json:"orders"`
	NetAmount           float64 `json:"net_amount"`
	DiscountedRows      int64   `json:"discounted_rows"`
	OverlapOrders       int64   `json:"overlap_orders"`
	OverlapNetAmount    float64 `json:"overlap_net_amount"`
	LabelRows           int64   `json:"label_rows"`
	LabelAmount         float64 `json:"label_amount"`
	PostedOverlapRows   int64   `json:"posted_overlap_rows"`
	PostedOverlapAmount float64 `json:"posted_overlap_amount"`
}

type ProfitReportStatus struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Timezone    string                `json:"timezone"`
	Sources     []ProfitReportSource  `json:"sources"`
	Coverage    []ProfitPriceCoverage `json:"coverage"`
	Pending     []ProfitPendingCheck  `json:"pending"`
	Sync        SyncStatus            `json:"sync"`
}

func (s *APIServer) profitReportStatus(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	data, err := s.store.profitReportStatus(ctx, s.timezone, strings.TrimSpace(request.URL.Query().Get("shop_key")))
	if err != nil {
		s.internalError(writer, "load financial report status", err)
		return
	}
	writeJSON(writer, http.StatusOK, apiResponse{Success: true, Data: data})
}

func (s *Store) profitReportStatus(ctx context.Context, timezone, shopKey string) (ProfitReportStatus, error) {
	result := ProfitReportStatus{GeneratedAt: time.Now().UTC(), Timezone: timezone,
		Sources: []ProfitReportSource{}, Coverage: []ProfitPriceCoverage{}, Pending: []ProfitPendingCheck{}}
	rows, err := s.db.QueryContext(ctx, `
		WITH sources AS (
		 SELECT shop_key,'settled' AS source,currency,received_at AS occurred_at,imported_at FROM temu_profit_settled_flows
		 UNION ALL SELECT shop_key,'shipping_'||billing_phase,currency,occurred_at,imported_at FROM temu_profit_shipping_label_fees
		 UNION ALL SELECT shop_key,'return_'||return_destination,currency,occurred_at,imported_at FROM temu_profit_return_label_fees
		 UNION ALL SELECT shop_key,'violation_'||source_sheet,currency,accounted_at,imported_at FROM temu_profit_fulfillment_violations
		 UNION ALL SELECT shop_key,'buyer_chargeback',currency,accounted_at,imported_at FROM temu_profit_buyer_chargebacks
		 UNION ALL SELECT shop_key,'platform_return',currency,accounted_at,imported_at FROM temu_profit_platform_return_label_fees
		 UNION ALL SELECT shop_key,'disposal',currency,accounted_at,imported_at FROM temu_profit_disposal_fees
		 UNION ALL SELECT shop_key,'unsettled',currency,NULL::timestamptz,imported_at FROM temu_profit_unsettle_orders
		)
		SELECT shop_key,source,currency,COUNT(*),MIN(occurred_at),MAX(occurred_at),MAX(imported_at)
		FROM sources WHERE ($1='' OR shop_key=$1) GROUP BY 1,2,3 ORDER BY 1,2,3
	`, shopKey)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var source ProfitReportSource
		if err := rows.Scan(&source.ShopKey, &source.Source, &source.Currency, &source.Rows,
			&source.FirstAt, &source.LastAt, &source.ImportedAt); err != nil {
			rows.Close()
			return result, err
		}
		result.Sources = append(result.Sources, source)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT o.shop_key,COUNT(*),COUNT(*) FILTER (WHERE l.unit_price IS NOT NULL),
		 COUNT(*) FILTER (WHERE l.unit_price IS NOT NULL AND e.match_method='exact_interval' AND e.status='confirmed'),
		 COUNT(*) FILTER (WHERE l.unit_price IS NOT NULL AND e.match_method IN ('nearest_before','nearest_after')),
		 COUNT(*) FILTER (WHERE l.unit_price IS NOT NULL AND l.unit_price_status='warning'),
		 COUNT(*) FILTER (WHERE o.occurred_at_source='first_seen'),MAX(l.unit_price_estimated_at)
		FROM normalized_orders o JOIN normalized_order_lines l ON l.order_id=o.id
		LEFT JOIN temu_order_line_price_estimates e ON e.order_line_id=l.id
		WHERE o.platform='temu' AND o.sales_eligible AND ($1='' OR o.shop_key=$1)
		GROUP BY 1 ORDER BY 1
	`, shopKey)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var coverage ProfitPriceCoverage
		if err := rows.Scan(&coverage.ShopKey, &coverage.Lines, &coverage.PricedLines, &coverage.ExactLines,
			&coverage.InferredLines, &coverage.WarningLines, &coverage.FirstSeenLines, &coverage.LatestBackfill); err != nil {
			rows.Close()
			return result, err
		}
		result.Coverage = append(result.Coverage, coverage)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}

	// Overlap is a review signal, not proof of full settlement. Do not silently
	// subtract it: an order can have both settled and pending transactions.
	rows, err = s.db.QueryContext(ctx, `
		WITH pending AS (
		 SELECT u.*,(sales_receipt+freight_receipt
		  -ABS(sales_chargeback)-ABS(freight_chargeback)) AS net_amount,
		 EXISTS (SELECT 1 FROM temu_profit_settled_flows f WHERE f.shop_key=u.shop_key AND f.po_no=u.po_no)
		 OR EXISTS (SELECT 1 FROM temu_profit_settled_parent_orders p WHERE p.shop_key=u.shop_key AND p.po_no=u.po_no) AS has_settlement
		 FROM temu_profit_unsettle_orders u WHERE ($1='' OR shop_key=$1)
		), orders AS (
		 SELECT shop_key,currency,COUNT(*) AS orders,SUM(net_amount) AS net_amount,
		 COUNT(*) FILTER (WHERE sales_receipt_after_discount<>0 OR freight_receipt_after_discount<>0) AS discounted_rows,
		 COUNT(*) FILTER (WHERE has_settlement) AS overlap_orders,COALESCE(SUM(net_amount) FILTER (WHERE has_settlement),0) AS overlap_net_amount
		 FROM pending GROUP BY 1,2
		), labels AS (
		 SELECT u.shop_key,u.currency,COUNT(*) AS label_rows,SUM(freight_amount) AS label_amount,
		 COUNT(*) FILTER (WHERE EXISTS (SELECT 1 FROM temu_profit_shipping_label_fees p WHERE p.billing_phase='posted'
		  AND p.shop_key=u.shop_key AND p.package_no=u.package_no AND p.tracking_no=u.tracking_no
		  AND p.bill_type=u.bill_type AND p.remark=u.remark)) AS posted_overlap_rows,
		 COALESCE(SUM(freight_amount) FILTER (WHERE EXISTS (SELECT 1 FROM temu_profit_shipping_label_fees p WHERE p.billing_phase='posted'
		  AND p.shop_key=u.shop_key AND p.package_no=u.package_no AND p.tracking_no=u.tracking_no
		  AND p.bill_type=u.bill_type AND p.remark=u.remark)),0) AS posted_overlap_amount
		 FROM temu_profit_shipping_label_fees u WHERE billing_phase='pending' AND ($1='' OR shop_key=$1) GROUP BY 1,2
		)
		SELECT COALESCE(o.shop_key,l.shop_key),COALESCE(o.currency,l.currency),COALESCE(orders,0),COALESCE(net_amount,0),
		 COALESCE(discounted_rows,0),COALESCE(overlap_orders,0),COALESCE(overlap_net_amount,0),COALESCE(label_rows,0),COALESCE(label_amount,0),
		 COALESCE(posted_overlap_rows,0),COALESCE(posted_overlap_amount,0)
		FROM orders o FULL JOIN labels l ON l.shop_key=o.shop_key AND l.currency=o.currency ORDER BY 1,2
	`, shopKey)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var check ProfitPendingCheck
		if err := rows.Scan(&check.ShopKey, &check.Currency, &check.Orders, &check.NetAmount, &check.DiscountedRows, &check.OverlapOrders,
			&check.OverlapNetAmount, &check.LabelRows, &check.LabelAmount, &check.PostedOverlapRows, &check.PostedOverlapAmount); err != nil {
			rows.Close()
			return result, err
		}
		result.Pending = append(result.Pending, check)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	result.Sync, err = s.latestSync(ctx)
	return result, err
}

type profitDailyLedgerCoverage struct {
	Date                       time.Time
	SettledRows, FeeRows       int64
	FeeBalance, DisposalAmount float64
}

func (s *Store) queryProfitDailyLedgerCoverage(ctx context.Context, timezone string, start, end time.Time, shopKey string) ([]profitDailyLedgerCoverage, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH ledger AS (
		 SELECT shop_key,received_at AS occurred_at,1 AS settled,0 AS fee,0::numeric AS amount,0::numeric AS disposal FROM temu_profit_settled_flows
		 UNION ALL SELECT shop_key,occurred_at,0,1,freight_amount,0 FROM temu_profit_shipping_label_fees WHERE billing_phase='posted'
		 UNION ALL SELECT shop_key,occurred_at,0,1,amount,0 FROM temu_profit_return_label_fees
		 UNION ALL SELECT shop_key,accounted_at,0,1,-ABS(amount),0 FROM temu_profit_fulfillment_violations
		 UNION ALL SELECT shop_key,accounted_at,0,1,-ABS(amount),0 FROM temu_profit_buyer_chargebacks
		 UNION ALL SELECT shop_key,accounted_at,0,1,amount,0 FROM temu_profit_platform_return_label_fees
		 UNION ALL SELECT shop_key,accounted_at,0,1,-ABS(destroy_fee),-ABS(destroy_fee) FROM temu_profit_disposal_fees
		)
		SELECT (occurred_at AT TIME ZONE $1)::date,SUM(settled),SUM(fee),SUM(amount),SUM(disposal)
		FROM ledger WHERE occurred_at >= $2 AND occurred_at < $3 AND ($4='' OR shop_key=$4)
		GROUP BY 1 ORDER BY 1
	`, timezone, start, end, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitDailyLedgerCoverage
	for rows.Next() {
		var row profitDailyLedgerCoverage
		if err := rows.Scan(&row.Date, &row.SettledRows, &row.FeeRows, &row.FeeBalance, &row.DisposalAmount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// Keep the import's original signed values, and expose the normalized balance
// separately. Costs and taxes that have no source remain absent, never zeroed.
func profitPlatformBalance(payback, feeBalance float64) float64 { return payback + feeBalance }
