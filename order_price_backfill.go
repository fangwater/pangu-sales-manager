package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"pangu-sales-manager/internal/marketing"
)

type OrderPriceBackfillRequest struct {
	StartAt time.Time `json:"start_at"`
	EndAt   time.Time `json:"end_at"`
	ShopKey string    `json:"shop_key,omitempty"`
	DryRun  bool      `json:"dry_run"`
}

type OrderPriceBackfillStats struct {
	StartAt         time.Time `json:"start_at"`
	EndAt           time.Time `json:"end_at"`
	ShopKey         string    `json:"shop_key,omitempty"`
	DryRun          bool      `json:"dry_run"`
	OrderLines      int       `json:"order_lines"`
	DistinctSKUs    int       `json:"distinct_skus"`
	ExactMatches    int       `json:"exact_matches"`
	InferredMatches int       `json:"inferred_matches"`
	Unmatched       int       `json:"unmatched"`
	Updated         int       `json:"updated"`
}

type orderLinePriceTarget struct {
	LineID           int64
	ProductSKUID     int64
	OccurredAt       time.Time
	OccurredAtSource string
}

func backfillOrderPriceEstimates(ctx context.Context, store *Store, observer *marketing.ActivityObserver, request OrderPriceBackfillRequest) (OrderPriceBackfillStats, error) {
	stats := OrderPriceBackfillStats{StartAt: request.StartAt.UTC(), EndAt: request.EndAt.UTC(), ShopKey: request.ShopKey, DryRun: request.DryRun}
	if request.StartAt.IsZero() || request.EndAt.IsZero() || !request.EndAt.After(request.StartAt) {
		return stats, fmt.Errorf("end_at must be after start_at")
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT l.id,
			COALESCE(l.platform_product_sku_id,NULLIF(l.raw_payload->'productList'->0->>'productSkuId','')::bigint),
			o.occurred_at,o.occurred_at_source
		FROM normalized_order_lines l
		JOIN normalized_orders o ON o.id=l.order_id
		WHERE o.platform='temu' AND o.occurred_at >= $1 AND o.occurred_at < $2
		  AND ($3='' OR o.shop_key=$3)
		ORDER BY l.id
	`, request.StartAt, request.EndAt, request.ShopKey)
	if err != nil {
		return stats, err
	}
	targets := make([]orderLinePriceTarget, 0)
	distinctSKUs := make(map[int64]struct{})
	for rows.Next() {
		var target orderLinePriceTarget
		var productSKU sql.NullInt64
		if err := rows.Scan(&target.LineID, &productSKU, &target.OccurredAt, &target.OccurredAtSource); err != nil {
			rows.Close()
			return stats, err
		}
		if productSKU.Valid {
			target.ProductSKUID = productSKU.Int64
			distinctSKUs[target.ProductSKUID] = struct{}{}
		}
		targets = append(targets, target)
	}
	if err := rows.Close(); err != nil {
		return stats, err
	}
	stats.OrderLines = len(targets)
	stats.DistinctSKUs = len(distinctSKUs)

	queryItems := make([]SKUPriceQueryItem, 0, len(targets))
	targetIndexes := make([]int, 0, len(targets))
	for index, target := range targets {
		if target.ProductSKUID <= 0 {
			continue
		}
		occurredAt := target.OccurredAt
		queryItems = append(queryItems, SKUPriceQueryItem{SKUID: target.ProductSKUID, At: &occurredAt})
		targetIndexes = append(targetIndexes, index)
	}
	resolved := make([]SKUPriceQueryResult, 0)
	if len(queryItems) > 0 {
		if len(queryItems) > maxSKUPriceQueryItems {
			for start := 0; start < len(queryItems); start += maxSKUPriceQueryItems {
				end := min(start+maxSKUPriceQueryItems, len(queryItems))
				batch, err := resolveSKUPriceQueries(ctx, observer, store, queryItems[start:end])
				if err != nil {
					return stats, err
				}
				resolved = append(resolved, batch...)
			}
		} else {
			resolved, err = resolveSKUPriceQueries(ctx, observer, store, queryItems)
			if err != nil {
				return stats, err
			}
		}
	}

	resultByTarget := make(map[int]SKUPriceQueryResult, len(resolved))
	for batchIndex, result := range resolved {
		if batchIndex < len(targetIndexes) {
			resultByTarget[targetIndexes[batchIndex]] = result
		}
	}
	for index := range targets {
		result, ok := resultByTarget[index]
		if !ok || result.MatchMethod == "unmatched" || result.Price <= 0 {
			stats.Unmatched++
			continue
		}
		if result.MatchMethod == "exact_interval" {
			stats.ExactMatches++
		} else {
			stats.InferredMatches++
		}
	}
	if request.DryRun {
		return stats, nil
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()
	for index, target := range targets {
		result, ok := resultByTarget[index]
		status := "unmatched"
		matchMethod := "unmatched"
		reason := "historical_sku_price_not_found"
		if target.ProductSKUID <= 0 {
			reason = "order_product_sku_id_missing"
		}
		if ok && result.MatchMethod != "unmatched" && result.Price > 0 {
			status = result.Status
			matchMethod = result.MatchMethod
			reason = result.Reason
			if matchMethod != "exact_interval" {
				status = marketing.SKCActivityWarning
			}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO temu_order_line_price_estimates(
				order_line_id,product_sku_id,skc_id,matched_interval_id,
				estimated_price,currency,status,price_source,match_method,
				order_time,order_time_source,interval_start_at,interval_end_at,
				time_distance_seconds,reason,estimated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,now())
			ON CONFLICT (order_line_id) DO UPDATE SET
				product_sku_id=EXCLUDED.product_sku_id,skc_id=EXCLUDED.skc_id,
				matched_interval_id=EXCLUDED.matched_interval_id,estimated_price=EXCLUDED.estimated_price,
				currency=EXCLUDED.currency,status=EXCLUDED.status,price_source=EXCLUDED.price_source,
				match_method=EXCLUDED.match_method,order_time=EXCLUDED.order_time,
				order_time_source=EXCLUDED.order_time_source,interval_start_at=EXCLUDED.interval_start_at,
				interval_end_at=EXCLUDED.interval_end_at,time_distance_seconds=EXCLUDED.time_distance_seconds,
				reason=EXCLUDED.reason,estimated_at=now()
		`, target.LineID, target.ProductSKUID, nullablePositiveInt64(result.SKCID), nullablePositiveInt64(result.IntervalID),
			result.Price, result.Currency, status, result.PriceSource, matchMethod,
			target.OccurredAt, target.OccurredAtSource, result.IntervalStartAt, result.IntervalEndAt,
			result.DistanceSeconds, reason)
		if err != nil {
			return stats, fmt.Errorf("save order line price estimate: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE normalized_order_lines SET
				platform_product_sku_id=COALESCE($2,platform_product_sku_id),
				unit_price=CASE WHEN $3::bigint>0 THEN $3::numeric/100 ELSE unit_price END,
				currency=CASE WHEN $4<>'' THEN $4 ELSE currency END,
				unit_price_source=$5,unit_price_status=$6,unit_price_estimated_at=now()
			WHERE id=$1
		`, target.LineID, nullablePositiveInt64(target.ProductSKUID), result.Price, result.Currency,
			"temu_"+matchMethod, status)
		if err != nil {
			return stats, fmt.Errorf("update normalized order line price: %w", err)
		}
		if result.Price > 0 {
			stats.Updated++
		}
	}
	if err := tx.Commit(); err != nil {
		return stats, err
	}
	return stats, nil
}
