package main

import (
	"context"
	"database/sql"
	"time"
)

// ProfitDailySummaryRow 是"利润总表"里的一行：某个统计桶（天/周/月）的销量、
// 退款分类计数、平台收入/支出明细。毛利润/毛利率需要成本数据，本期暂不提供。
//
// 税金代扣 / 税金退回 / 非订单交易费三列目前没有任何导入数据源——样例的
// 对账中心-账务明细工作簿里只有六张 sheet（结算、支出-买家拒付、支出-履约
// 违规-1/2、其他-退货面单费平台承担、支出-处置费），不存在这三类。所以这三
// 列是 *float64，值恒为 nil，前端展示"--"，等确认了数据来源再接。
type ProfitDailySummaryRow struct {
	Bucket string  `json:"bucket"`
	Label  string  `json:"label"`
	Units  float64 `json:"units"`

	RefundTotal        int64 `json:"refund_total"`
	RefundOrders       int64 `json:"refund_orders"`
	SalesChargebacks   int64 `json:"sales_chargebacks"`
	FreightChargebacks int64 `json:"freight_chargebacks"`
	BuyerChargebacks   int64 `json:"buyer_chargebacks"`

	PaybackAmount float64 `json:"payback_amount"` // 结算净额（收入-冲回），沿用初版口径

	// 平台收入：均取自 temu_profit_settled_flows（对账中心-账务明细·结算 /
	// 结算数据-已到账款项-PO明细账单），因为聚合账单/待处理款项两张表没有日期列，
	// 无法按天统计，只能以账务明细结算为基准。
	SalesReceiptAmount   float64 `json:"sales_receipt_amount"`
	FreightReceiptAmount float64 `json:"freight_receipt_amount"`

	// 平台费用：面单与平台补偿保留收支方向；违规、拒付及处置的
	// 支出金额统一按负数汇总，原始源表数值不变。
	SalesChargebackAmount          float64  `json:"sales_chargeback_amount"`
	FreightChargebackAmount        float64  `json:"freight_chargeback_amount"`
	FulfillmentDelayAmount         float64  `json:"fulfillment_delay_amount"`
	FulfillmentFalseShipAmount     float64  `json:"fulfillment_false_ship_amount"`
	BuyerChargebackAmount          float64  `json:"buyer_chargeback_amount"`
	ShippingLabelFeeAmount         float64  `json:"shipping_label_fee_amount"`
	ReturnLabelFeeMerchantAmount   float64  `json:"return_label_fee_merchant_amount"`
	ReturnLabelFeeThirdPartyAmount float64  `json:"return_label_fee_third_party_amount"`
	PlatformReturnLabelFeeAmount   float64  `json:"platform_return_label_fee_amount"`
	TaxWithheldAmount              *float64 `json:"tax_withheld_amount"`
	TaxRefundAmount                *float64 `json:"tax_refund_amount"`
	NonOrderTransactionFeeAmount   *float64 `json:"non_order_transaction_fee_amount"`

	// EstimatedSalesAmount 是根据订单数量 * 活动价格回填的 unit_price 推断出的
	// 销售额，跟上面结算类数据完全独立、不汇总进 PaybackAmount。回填覆盖率低时
	// EstimatedSalesMatched/EstimatedSalesTotal 会明显小于订单行总数，前端要把
	// 覆盖率展示出来，不能让人误以为这是完整数字。
	EstimatedSalesAmount  float64 `json:"estimated_sales_amount"`
	EstimatedSalesMatched int64   `json:"estimated_sales_matched"`
	EstimatedSalesTotal   int64   `json:"estimated_sales_total"`
	SettledRows           int64   `json:"settled_rows"`
	FeeRows               int64   `json:"fee_rows"`
	KnownFeeBalanceAmount float64 `json:"known_fee_balance_amount"`
	DisposalFeeAmount     float64 `json:"disposal_fee_amount"`
	PlatformBalanceAmount float64 `json:"platform_balance_amount"`
}

type ProfitDailySummaryResponse struct {
	Period string                  `json:"period"`
	Range  DateRange               `json:"range"`
	Rows   []ProfitDailySummaryRow `json:"rows"`
	Totals ProfitDailySummaryRow   `json:"totals"`
}

type profitDailyUnit struct {
	Date  time.Time
	Units float64
}

type profitDailyCount struct {
	Date  time.Time
	Count int64
}

type profitDailySettled struct {
	Date                    time.Time
	SalesChargebacks        int64
	FreightChargebacks      int64
	PaybackAmount           float64
	SalesReceiptAmount      float64
	FreightReceiptAmount    float64
	SalesChargebackAmount   float64
	FreightChargebackAmount float64
}

type profitDailyCountAmount struct {
	Date   time.Time
	Count  int64
	Amount float64
}

type profitDailyViolations struct {
	Date      time.Time
	Delay     float64
	FalseShip float64
}

type profitDailyReturns struct {
	Date       time.Time
	Merchant   float64
	ThirdParty float64
}

type profitDailyAmount struct {
	Date   time.Time
	Amount float64
}

type profitDailyEstimate struct {
	Date    time.Time
	Amount  float64
	Matched int64
	Total   int64
}

// profitDailySummary 汇总 TEMU 销量 / 退款分类 / 平台收入 / 平台支出，按 period 分桶。
// 复用 analytics.go 里的 periodWindows/bucketStart/nextBucket/bucketLabel。
func (s *Store) profitDailySummary(ctx context.Context, timezone, period, shopKey string) (ProfitDailySummaryResponse, error) {
	if period != "day" && period != "week" && period != "month" {
		period = "day"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}
	now := time.Now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	end := today.AddDate(0, 0, 1)

	// Unlike the live sales dashboard, profit data arrives in bounded historical
	// import batches rather than a continuous trailing window, so the range
	// shown here is driven by what's actually in the data instead of an
	// arbitrary "last N days/weeks/months" cutoff from today.
	dataStart, found, err := s.queryProfitDataRangeStart(ctx, location, shopKey)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}
	start := today
	if found {
		start = dataStart
	}

	units, err := s.queryProfitDailyUnits(ctx, timezone, start, end, shopKey)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}
	refundOrders, err := s.queryProfitDailyOrderRefunds(ctx, timezone, start, end, shopKey)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}
	settled, err := s.queryProfitDailySettledFlow(ctx, timezone, start, end, shopKey)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}
	buyerChargebacks, err := s.queryProfitDailyBuyerChargebacks(ctx, timezone, start, end, shopKey)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}
	violations, err := s.queryProfitDailyFulfillmentViolations(ctx, timezone, start, end, shopKey)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}
	shippingFees, err := s.queryProfitDailyShippingLabelFees(ctx, timezone, start, end, shopKey)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}
	returnFees, err := s.queryProfitDailyReturnLabelFees(ctx, timezone, start, end, shopKey)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}
	platformReturnFees, err := s.queryProfitDailyPlatformReturnLabelFees(ctx, timezone, start, end, shopKey)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}
	estimatedSales, err := s.queryProfitDailyEstimatedSales(ctx, timezone, start, end, shopKey)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}
	ledgerCoverage, err := s.queryProfitDailyLedgerCoverage(ctx, timezone, start, end, shopKey)
	if err != nil {
		return ProfitDailySummaryResponse{}, err
	}

	buckets := make(map[string]*ProfitDailySummaryRow)
	var order []string
	for bucket := bucketStart(period, start); bucket.Before(end); bucket = nextBucket(period, bucket) {
		key := bucket.Format("2006-01-02")
		buckets[key] = &ProfitDailySummaryRow{Bucket: key, Label: bucketLabel(period, bucket)}
		order = append(order, key)
	}
	rowFor := func(date time.Time) *ProfitDailySummaryRow {
		return buckets[bucketStart(period, date).Format("2006-01-02")]
	}

	var totals ProfitDailySummaryRow
	for _, u := range units {
		if row := rowFor(u.Date); row != nil {
			row.Units += u.Units
			totals.Units += u.Units
		}
	}
	for _, c := range refundOrders {
		if row := rowFor(c.Date); row != nil {
			row.RefundOrders += c.Count
			totals.RefundOrders += c.Count
		}
	}
	for _, sf := range settled {
		if row := rowFor(sf.Date); row != nil {
			row.SalesChargebacks += sf.SalesChargebacks
			row.FreightChargebacks += sf.FreightChargebacks
			row.PaybackAmount += sf.PaybackAmount
			row.SalesReceiptAmount += sf.SalesReceiptAmount
			row.FreightReceiptAmount += sf.FreightReceiptAmount
			row.SalesChargebackAmount += sf.SalesChargebackAmount
			row.FreightChargebackAmount += sf.FreightChargebackAmount
			totals.SalesChargebacks += sf.SalesChargebacks
			totals.FreightChargebacks += sf.FreightChargebacks
			totals.PaybackAmount += sf.PaybackAmount
			totals.SalesReceiptAmount += sf.SalesReceiptAmount
			totals.FreightReceiptAmount += sf.FreightReceiptAmount
			totals.SalesChargebackAmount += sf.SalesChargebackAmount
			totals.FreightChargebackAmount += sf.FreightChargebackAmount
		}
	}
	for _, c := range buyerChargebacks {
		if row := rowFor(c.Date); row != nil {
			row.BuyerChargebacks += c.Count
			row.BuyerChargebackAmount += c.Amount
			totals.BuyerChargebacks += c.Count
			totals.BuyerChargebackAmount += c.Amount
		}
	}
	for _, v := range violations {
		if row := rowFor(v.Date); row != nil {
			row.FulfillmentDelayAmount += v.Delay
			row.FulfillmentFalseShipAmount += v.FalseShip
			totals.FulfillmentDelayAmount += v.Delay
			totals.FulfillmentFalseShipAmount += v.FalseShip
		}
	}
	for _, f := range shippingFees {
		if row := rowFor(f.Date); row != nil {
			row.ShippingLabelFeeAmount += f.Amount
			totals.ShippingLabelFeeAmount += f.Amount
		}
	}
	for _, r := range returnFees {
		if row := rowFor(r.Date); row != nil {
			row.ReturnLabelFeeMerchantAmount += r.Merchant
			row.ReturnLabelFeeThirdPartyAmount += r.ThirdParty
			totals.ReturnLabelFeeMerchantAmount += r.Merchant
			totals.ReturnLabelFeeThirdPartyAmount += r.ThirdParty
		}
	}
	for _, p := range platformReturnFees {
		if row := rowFor(p.Date); row != nil {
			row.PlatformReturnLabelFeeAmount += p.Amount
			totals.PlatformReturnLabelFeeAmount += p.Amount
		}
	}
	for _, e := range estimatedSales {
		if row := rowFor(e.Date); row != nil {
			row.EstimatedSalesAmount += e.Amount
			row.EstimatedSalesMatched += e.Matched
			row.EstimatedSalesTotal += e.Total
			totals.EstimatedSalesAmount += e.Amount
			totals.EstimatedSalesMatched += e.Matched
			totals.EstimatedSalesTotal += e.Total
		}
	}

	rows := make([]ProfitDailySummaryRow, 0, len(order))
	for _, coverage := range ledgerCoverage {
		if row := rowFor(coverage.Date); row != nil {
			row.SettledRows += coverage.SettledRows
			row.FeeRows += coverage.FeeRows
			row.KnownFeeBalanceAmount += coverage.FeeBalance
			row.DisposalFeeAmount += coverage.DisposalAmount
			totals.SettledRows += coverage.SettledRows
			totals.FeeRows += coverage.FeeRows
			totals.KnownFeeBalanceAmount += coverage.FeeBalance
			totals.DisposalFeeAmount += coverage.DisposalAmount
		}
	}
	for _, key := range order {
		row := buckets[key]
		row.PlatformBalanceAmount = profitPlatformBalance(row.PaybackAmount, row.KnownFeeBalanceAmount)
		row.RefundTotal = row.RefundOrders + row.SalesChargebacks + row.FreightChargebacks + row.BuyerChargebacks
		rows = append(rows, *row)
	}
	totals.RefundTotal = totals.RefundOrders + totals.SalesChargebacks + totals.FreightChargebacks + totals.BuyerChargebacks
	totals.PlatformBalanceAmount = profitPlatformBalance(totals.PaybackAmount, totals.KnownFeeBalanceAmount)

	return ProfitDailySummaryResponse{
		Period: period,
		Range:  DateRange{Start: start.Format("2006-01-02"), End: today.Format("2006-01-02")},
		Rows:   rows,
		Totals: totals,
	}, nil
}

// queryProfitDataRangeStart finds the earliest date-bucket boundary across
// every table this summary draws from, so the shown range reflects what was
// actually imported instead of a fixed trailing window from today.
func (s *Store) queryProfitDataRangeStart(ctx context.Context, location *time.Location, shopKey string) (time.Time, bool, error) {
	var earliest sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT MIN(d) FROM (
			SELECT occurred_at AS d FROM normalized_orders WHERE platform='temu' AND ($1='' OR shop_key=$1)
			UNION ALL
			SELECT received_at FROM temu_profit_settled_flows WHERE received_at IS NOT NULL AND ($1='' OR shop_key=$1)
			UNION ALL
			SELECT accounted_at FROM temu_profit_buyer_chargebacks WHERE accounted_at IS NOT NULL AND ($1='' OR shop_key=$1)
			UNION ALL
			SELECT accounted_at FROM temu_profit_fulfillment_violations WHERE accounted_at IS NOT NULL AND ($1='' OR shop_key=$1)
			UNION ALL
			SELECT occurred_at FROM temu_profit_shipping_label_fees WHERE billing_phase='posted' AND occurred_at IS NOT NULL AND ($1='' OR shop_key=$1)
			UNION ALL
			SELECT occurred_at FROM temu_profit_return_label_fees WHERE occurred_at IS NOT NULL AND ($1='' OR shop_key=$1)
			UNION ALL
			SELECT accounted_at FROM temu_profit_platform_return_label_fees WHERE accounted_at IS NOT NULL AND ($1='' OR shop_key=$1)
			UNION ALL
			SELECT accounted_at FROM temu_profit_disposal_fees WHERE accounted_at IS NOT NULL AND ($1='' OR shop_key=$1)
		) sources
	`, shopKey).Scan(&earliest)
	if err != nil {
		return time.Time{}, false, err
	}
	if !earliest.Valid {
		return time.Time{}, false, nil
	}
	local := earliest.Time.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location), true, nil
}

func (s *Store) queryProfitDailyUnits(ctx context.Context, timezone string, start, end time.Time, shopKey string) ([]profitDailyUnit, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (o.occurred_at AT TIME ZONE $1)::date, COALESCE(SUM(l.quantity), 0)
		FROM normalized_orders o
		JOIN normalized_order_lines l ON l.order_id=o.id
		WHERE o.platform='temu' AND o.sales_eligible
		  AND o.occurred_at >= $2 AND o.occurred_at < $3
		  AND ($4='' OR o.shop_key=$4)
		GROUP BY 1 ORDER BY 1
	`, timezone, start, end, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitDailyUnit
	for rows.Next() {
		var row profitDailyUnit
		if err := rows.Scan(&row.Date, &row.Units); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// queryProfitDailyEstimatedSales infers a sales amount from order quantity *
// unit_price, where unit_price has been backfilled from the activity price
// estimation pipeline (order_price_backfill.go). Coverage is currently very
// partial, so Matched/Total are returned alongside the amount so callers can
// show how much of the day's order lines actually contributed.
func (s *Store) queryProfitDailyEstimatedSales(ctx context.Context, timezone string, start, end time.Time, shopKey string) ([]profitDailyEstimate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (o.occurred_at AT TIME ZONE $1)::date,
		       COALESCE(SUM(l.quantity * l.unit_price) FILTER (WHERE l.unit_price IS NOT NULL), 0),
		       COUNT(*) FILTER (WHERE l.unit_price IS NOT NULL),
		       COUNT(*)
		FROM normalized_orders o
		JOIN normalized_order_lines l ON l.order_id=o.id
		WHERE o.platform='temu' AND o.sales_eligible
		  AND o.occurred_at >= $2 AND o.occurred_at < $3
		  AND ($4='' OR o.shop_key=$4)
		GROUP BY 1 ORDER BY 1
	`, timezone, start, end, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitDailyEstimate
	for rows.Next() {
		var row profitDailyEstimate
		if err := rows.Scan(&row.Date, &row.Amount, &row.Matched, &row.Total); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryProfitDailyOrderRefunds(ctx context.Context, timezone string, start, end time.Time, shopKey string) ([]profitDailyCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (o.occurred_at AT TIME ZONE $1)::date, COUNT(*)
		FROM normalized_orders o
		WHERE o.platform='temu' AND NOT o.sales_eligible
		  AND o.occurred_at >= $2 AND o.occurred_at < $3
		  AND ($4='' OR o.shop_key=$4)
		GROUP BY 1 ORDER BY 1
	`, timezone, start, end, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitDailyCount
	for rows.Next() {
		var row profitDailyCount
		if err := rows.Scan(&row.Date, &row.Count); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryProfitDailySettledFlow(ctx context.Context, timezone string, start, end time.Time, shopKey string) ([]profitDailySettled, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (f.received_at AT TIME ZONE $1)::date,
		       COUNT(*) FILTER (WHERE f.trade_type='销售冲回'),
		       COUNT(*) FILTER (WHERE f.trade_type='运费冲回'),
		       COALESCE(SUM(f.settle_amount), 0),
		       COALESCE(SUM(f.settle_amount) FILTER (WHERE f.trade_type='销售回款'), 0),
		       COALESCE(SUM(f.settle_amount) FILTER (WHERE f.trade_type='运费回款'), 0),
		       COALESCE(SUM(f.settle_amount) FILTER (WHERE f.trade_type='销售冲回'), 0),
		       COALESCE(SUM(f.settle_amount) FILTER (WHERE f.trade_type='运费冲回'), 0)
		FROM temu_profit_settled_flows f
		WHERE f.received_at IS NOT NULL
		  AND f.received_at >= $2 AND f.received_at < $3
		  AND ($4='' OR f.shop_key=$4)
		GROUP BY 1 ORDER BY 1
	`, timezone, start, end, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitDailySettled
	for rows.Next() {
		var row profitDailySettled
		if err := rows.Scan(&row.Date, &row.SalesChargebacks, &row.FreightChargebacks, &row.PaybackAmount,
			&row.SalesReceiptAmount, &row.FreightReceiptAmount, &row.SalesChargebackAmount, &row.FreightChargebackAmount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryProfitDailyBuyerChargebacks(ctx context.Context, timezone string, start, end time.Time, shopKey string) ([]profitDailyCountAmount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (c.accounted_at AT TIME ZONE $1)::date, COUNT(*), COALESCE(SUM(-ABS(c.amount)), 0)
		FROM temu_profit_buyer_chargebacks c
		WHERE c.accounted_at IS NOT NULL
		  AND c.accounted_at >= $2 AND c.accounted_at < $3
		  AND ($4='' OR c.shop_key=$4)
		GROUP BY 1 ORDER BY 1
	`, timezone, start, end, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitDailyCountAmount
	for rows.Next() {
		var row profitDailyCountAmount
		if err := rows.Scan(&row.Date, &row.Count, &row.Amount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryProfitDailyFulfillmentViolations(ctx context.Context, timezone string, start, end time.Time, shopKey string) ([]profitDailyViolations, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (v.accounted_at AT TIME ZONE $1)::date,
		       COALESCE(SUM(-ABS(v.amount)) FILTER (WHERE v.source_sheet='fulfillment_delay'), 0),
		       COALESCE(SUM(-ABS(v.amount)) FILTER (WHERE v.source_sheet='fulfillment_false_ship'), 0)
		FROM temu_profit_fulfillment_violations v
		WHERE v.accounted_at IS NOT NULL
		  AND v.accounted_at >= $2 AND v.accounted_at < $3
		  AND ($4='' OR v.shop_key=$4)
		GROUP BY 1 ORDER BY 1
	`, timezone, start, end, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitDailyViolations
	for rows.Next() {
		var row profitDailyViolations
		if err := rows.Scan(&row.Date, &row.Delay, &row.FalseShip); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryProfitDailyShippingLabelFees(ctx context.Context, timezone string, start, end time.Time, shopKey string) ([]profitDailyAmount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (sh.occurred_at AT TIME ZONE $1)::date, COALESCE(SUM(sh.freight_amount), 0)
		FROM temu_profit_shipping_label_fees sh
		WHERE sh.billing_phase='posted' AND sh.occurred_at IS NOT NULL
		  AND sh.occurred_at >= $2 AND sh.occurred_at < $3
		  AND ($4='' OR sh.shop_key=$4)
		GROUP BY 1 ORDER BY 1
	`, timezone, start, end, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitDailyAmount
	for rows.Next() {
		var row profitDailyAmount
		if err := rows.Scan(&row.Date, &row.Amount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryProfitDailyReturnLabelFees(ctx context.Context, timezone string, start, end time.Time, shopKey string) ([]profitDailyReturns, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (r.occurred_at AT TIME ZONE $1)::date,
		       COALESCE(SUM(r.amount) FILTER (WHERE r.return_destination='merchant_warehouse'), 0),
		       COALESCE(SUM(r.amount) FILTER (WHERE r.return_destination='third_party_warehouse'), 0)
		FROM temu_profit_return_label_fees r
		WHERE r.occurred_at IS NOT NULL
		  AND r.occurred_at >= $2 AND r.occurred_at < $3
		  AND ($4='' OR r.shop_key=$4)
		GROUP BY 1 ORDER BY 1
	`, timezone, start, end, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitDailyReturns
	for rows.Next() {
		var row profitDailyReturns
		if err := rows.Scan(&row.Date, &row.Merchant, &row.ThirdParty); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) queryProfitDailyPlatformReturnLabelFees(ctx context.Context, timezone string, start, end time.Time, shopKey string) ([]profitDailyAmount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (p.accounted_at AT TIME ZONE $1)::date, COALESCE(SUM(p.amount), 0)
		FROM temu_profit_platform_return_label_fees p
		WHERE p.accounted_at IS NOT NULL
		  AND p.accounted_at >= $2 AND p.accounted_at < $3
		  AND ($4='' OR p.shop_key=$4)
		GROUP BY 1 ORDER BY 1
	`, timezone, start, end, shopKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []profitDailyAmount
	for rows.Next() {
		var row profitDailyAmount
		if err := rows.Scan(&row.Date, &row.Amount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
