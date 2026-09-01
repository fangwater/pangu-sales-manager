package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func (s *Store) beginProfitImport(ctx context.Context, shopKey, sourceKind, sourceName string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO temu_profit_import_runs(shop_key, source_kind, source_name, status)
		VALUES ($1,$2,$3,'running') RETURNING id
	`, shopKey, sourceKind, sourceName).Scan(&id)
	return id, err
}

func (s *Store) finishProfitImport(ctx context.Context, id int64, status string, result ProfitImportResult, importErr error) error {
	errorMessage := ""
	if importErr != nil {
		errorMessage = importErr.Error()
	}
	payload, err := json.Marshal(result)
	if err != nil {
		payload = []byte(`{}`)
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE temu_profit_import_runs
		SET status=$2, completed_at=now(), files_imported=$3, rows_upserted=$4,
		    result_json=$5, error_message=$6
		WHERE id=$1
	`, id, status, result.FilesImported, result.RowsUpserted, payload, errorMessage)
	return err
}

func (s *Store) latestProfitImport(ctx context.Context) (*ProfitImportRun, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, shop_key, source_kind, source_name, status, files_imported, rows_upserted,
		       result_json, error_message, started_at, completed_at
		FROM temu_profit_import_runs
		ORDER BY started_at DESC, id DESC
		LIMIT 1
	`)
	var item ProfitImportRun
	var payload []byte
	var completed sql.NullTime
	if err := row.Scan(&item.ID, &item.ShopKey, &item.SourceKind, &item.SourceName, &item.Status,
		&item.FilesImported, &item.RowsUpserted, &payload, &item.ErrorMessage, &item.StartedAt, &completed); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if completed.Valid {
		item.CompletedAt = &completed.Time
	}
	if len(payload) > 0 && string(payload) != "{}" {
		var result ProfitImportResult
		if err := json.Unmarshal(payload, &result); err == nil {
			item.Result = &result
		}
	}
	return &item, nil
}

func (s *Store) profitTableCounts(ctx context.Context) ([]ProfitTableCount, error) {
	queries := []ProfitTableCount{
		{Table: "temu_profit_shipping_label_fees", Label: "发货面单费"},
		{Table: "temu_profit_return_label_fees", Label: "退货面单费"},
		{Table: "temu_profit_buyer_chargebacks", Label: "买家拒付"},
		{Table: "temu_profit_fulfillment_violations", Label: "履约违规"},
		{Table: "temu_profit_platform_return_label_fees", Label: "退货面单费平台承担"},
		{Table: "temu_profit_disposal_fees", Label: "处置费"},
		{Table: "temu_profit_settled_flows", Label: "结算流水"},
		{Table: "temu_profit_settled_parent_orders", Label: "已到账聚合账单"},
		{Table: "temu_profit_settled_parent_skus", Label: "已到账聚合账单 SKU"},
		{Table: "temu_profit_unsettle_orders", Label: "待处理款项"},
		{Table: "temu_profit_unsettle_skus", Label: "待处理款项 SKU"},
	}
	for i := range queries {
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+queries[i].Table).Scan(&queries[i].Rows); err != nil {
			return nil, err
		}
	}
	return queries, nil
}

func (s *Store) applyProfitImport(ctx context.Context, shopKey string, batches []ProfitImportBatch) (ProfitImportResult, error) {
	result := ProfitImportResult{ShopKey: shopKey, Files: []ProfitFileImportResult{}, Tables: []ProfitTableStat{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	totals := map[string]*ProfitTableStat{}
	for _, batch := range batches {
		fileResult := ProfitFileImportResult{
			File: batch.File, Kind: batch.Kind, Label: profitFileLabels[batch.Kind], Warnings: batch.Warnings,
		}
		stats, err := upsertProfitBatch(ctx, tx, shopKey, batch)
		if err != nil {
			return result, fmt.Errorf("%s: %w", batch.File, err)
		}
		fileResult.Tables = stats
		result.Files = append(result.Files, fileResult)
		result.FilesImported++
		for _, stat := range stats {
			result.RowsUpserted += stat.Inserted + stat.Updated
			acc := totals[stat.Table]
			if acc == nil {
				copyStat := stat
				totals[stat.Table] = &copyStat
				continue
			}
			acc.Rows += stat.Rows
			acc.Inserted += stat.Inserted
			acc.Updated += stat.Updated
		}
		result.Warnings = append(result.Warnings, batch.Warnings...)
	}
	for _, stat := range totals {
		result.Tables = append(result.Tables, *stat)
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func upsertProfitBatch(ctx context.Context, tx *sql.Tx, shopKey string, batch ProfitImportBatch) ([]ProfitTableStat, error) {
	var stats []ProfitTableStat
	add := func(stat ProfitTableStat, err error) error {
		if err != nil {
			return err
		}
		if stat.Rows > 0 {
			stats = append(stats, stat)
		}
		return nil
	}
	if err := add(upsertShippingLabelFees(ctx, tx, shopKey, batch.Shipping)); err != nil {
		return nil, err
	}
	if err := add(upsertReturnLabelFees(ctx, tx, shopKey, batch.Returns)); err != nil {
		return nil, err
	}
	if err := add(upsertBuyerChargebacks(ctx, tx, shopKey, batch.Chargebacks)); err != nil {
		return nil, err
	}
	if err := add(upsertFulfillmentViolations(ctx, tx, shopKey, batch.Violations)); err != nil {
		return nil, err
	}
	if err := add(upsertPlatformReturnLabelFees(ctx, tx, shopKey, batch.Platforms)); err != nil {
		return nil, err
	}
	if err := add(upsertDisposalFees(ctx, tx, shopKey, batch.Disposals)); err != nil {
		return nil, err
	}
	if err := add(upsertSettledFlows(ctx, tx, shopKey, batch.Settled)); err != nil {
		return nil, err
	}
	if err := add(upsertSettledParentOrders(ctx, tx, shopKey, batch.Parents)); err != nil {
		return nil, err
	}
	if err := add(upsertUnsettleOrders(ctx, tx, shopKey, batch.Unsettles)); err != nil {
		return nil, err
	}
	return stats, nil
}

func upsertShippingLabelFees(ctx context.Context, tx *sql.Tx, shopKey string, items []TemuProfitShippingLabelFee) (ProfitTableStat, error) {
	stat := ProfitTableStat{Table: "temu_profit_shipping_label_fees", Label: "发货面单费", Rows: len(items)}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO temu_profit_shipping_label_fees(
			shop_key, billing_phase, package_no, tracking_no, carrier_code, bill_type, remark,
			freight_amount, currency, reconcile_status, occurred_at, imported_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
		ON CONFLICT (shop_key, billing_phase, package_no, tracking_no, bill_type, remark) DO UPDATE SET
			carrier_code=EXCLUDED.carrier_code, freight_amount=EXCLUDED.freight_amount,
			currency=EXCLUDED.currency, reconcile_status=EXCLUDED.reconcile_status,
			occurred_at=EXCLUDED.occurred_at, imported_at=now()
		RETURNING (xmax = 0)
	`)
	if err != nil {
		return stat, err
	}
	defer stmt.Close()
	for _, item := range items {
		inserted, err := scanInserted(stmt.QueryRowContext(ctx, coalesceShop(shopKey, item.ShopKey), item.BillingPhase,
			item.PackageNo, item.TrackingNo, item.CarrierCode, item.BillType, item.Remark,
			item.FreightAmount, item.Currency, item.ReconcileStatus, nullableTimePtr(item.OccurredAt)))
		if err != nil {
			return stat, fmt.Errorf("发货面单费 %s: %w", item.PackageNo, err)
		}
		tally(&stat, inserted)
	}
	return stat, nil
}

func upsertReturnLabelFees(ctx context.Context, tx *sql.Tx, shopKey string, items []TemuProfitReturnLabelFee) (ProfitTableStat, error) {
	stat := ProfitTableStat{Table: "temu_profit_return_label_fees", Label: "退货面单费", Rows: len(items)}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO temu_profit_return_label_fees(
			shop_key, return_destination, fund_bill_id, tracking_no, po_no, bill_type,
			currency, bill_remark, amount, occurred_at, imported_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())
		ON CONFLICT (shop_key, fund_bill_id) DO UPDATE SET
			return_destination=EXCLUDED.return_destination, tracking_no=EXCLUDED.tracking_no,
			po_no=EXCLUDED.po_no, bill_type=EXCLUDED.bill_type, currency=EXCLUDED.currency,
			bill_remark=EXCLUDED.bill_remark, amount=EXCLUDED.amount, occurred_at=EXCLUDED.occurred_at,
			imported_at=now()
		RETURNING (xmax = 0)
	`)
	if err != nil {
		return stat, err
	}
	defer stmt.Close()
	for _, item := range items {
		inserted, err := scanInserted(stmt.QueryRowContext(ctx, coalesceShop(shopKey, item.ShopKey), item.ReturnDestination,
			item.FundBillID, item.TrackingNo, item.PONo, item.BillType, item.Currency, item.BillRemark,
			item.Amount, nullableTimePtr(item.OccurredAt)))
		if err != nil {
			return stat, fmt.Errorf("退货面单费 %s: %w", item.FundBillID, err)
		}
		tally(&stat, inserted)
	}
	return stat, nil
}

func upsertBuyerChargebacks(ctx context.Context, tx *sql.Tx, shopKey string, items []TemuProfitBuyerChargeback) (ProfitTableStat, error) {
	stat := ProfitTableStat{Table: "temu_profit_buyer_chargebacks", Label: "买家拒付", Rows: len(items)}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO temu_profit_buyer_chargebacks(shop_key, violation_order_no, amount, currency, accounted_at, imported_at)
		VALUES ($1,$2,$3,$4,$5,now())
		ON CONFLICT (shop_key, violation_order_no) DO UPDATE SET
			amount=EXCLUDED.amount, currency=EXCLUDED.currency, accounted_at=EXCLUDED.accounted_at, imported_at=now()
		RETURNING (xmax = 0)
	`)
	if err != nil {
		return stat, err
	}
	defer stmt.Close()
	for _, item := range items {
		inserted, err := scanInserted(stmt.QueryRowContext(ctx, coalesceShop(shopKey, item.ShopKey), item.ViolationOrderNo,
			item.Amount, item.Currency, nullableTimePtr(item.AccountedAt)))
		if err != nil {
			return stat, fmt.Errorf("买家拒付 %s: %w", item.ViolationOrderNo, err)
		}
		tally(&stat, inserted)
	}
	return stat, nil
}

func upsertFulfillmentViolations(ctx context.Context, tx *sql.Tx, shopKey string, items []TemuProfitFulfillmentViolation) (ProfitTableStat, error) {
	stat := ProfitTableStat{Table: "temu_profit_fulfillment_violations", Label: "履约违规", Rows: len(items)}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO temu_profit_fulfillment_violations(
			shop_key, source_sheet, violation_no, order_no, violation_type, amount, currency, accounted_at, imported_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
		ON CONFLICT (shop_key, violation_no) DO UPDATE SET
			source_sheet=EXCLUDED.source_sheet, order_no=EXCLUDED.order_no, violation_type=EXCLUDED.violation_type,
			amount=EXCLUDED.amount, currency=EXCLUDED.currency, accounted_at=EXCLUDED.accounted_at, imported_at=now()
		RETURNING (xmax = 0)
	`)
	if err != nil {
		return stat, err
	}
	defer stmt.Close()
	for _, item := range items {
		inserted, err := scanInserted(stmt.QueryRowContext(ctx, coalesceShop(shopKey, item.ShopKey), item.SourceSheet,
			item.ViolationNo, item.OrderNo, item.ViolationType, item.Amount, item.Currency, nullableTimePtr(item.AccountedAt)))
		if err != nil {
			return stat, fmt.Errorf("履约违规 %s: %w", item.ViolationNo, err)
		}
		tally(&stat, inserted)
	}
	return stat, nil
}

func upsertPlatformReturnLabelFees(ctx context.Context, tx *sql.Tx, shopKey string, items []TemuProfitPlatformReturnLabelFee) (ProfitTableStat, error) {
	stat := ProfitTableStat{Table: "temu_profit_platform_return_label_fees", Label: "退货面单费平台承担", Rows: len(items)}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO temu_profit_platform_return_label_fees(shop_key, order_no, amount, currency, remark, accounted_at, imported_at)
		VALUES ($1,$2,$3,$4,$5,$6,now())
		ON CONFLICT (shop_key, order_no, remark) DO UPDATE SET
			amount=EXCLUDED.amount, currency=EXCLUDED.currency, accounted_at=EXCLUDED.accounted_at, imported_at=now()
		RETURNING (xmax = 0)
	`)
	if err != nil {
		return stat, err
	}
	defer stmt.Close()
	for _, item := range items {
		inserted, err := scanInserted(stmt.QueryRowContext(ctx, coalesceShop(shopKey, item.ShopKey), item.OrderNo,
			item.Amount, item.Currency, item.Remark, nullableTimePtr(item.AccountedAt)))
		if err != nil {
			return stat, fmt.Errorf("平台承担退货面单费 %s: %w", item.OrderNo, err)
		}
		tally(&stat, inserted)
	}
	return stat, nil
}

func upsertDisposalFees(ctx context.Context, tx *sql.Tx, shopKey string, items []TemuProfitDisposalFee) (ProfitTableStat, error) {
	stat := ProfitTableStat{Table: "temu_profit_disposal_fees", Label: "处置费", Rows: len(items)}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO temu_profit_disposal_fees(
			shop_key, sku_id, tracking_type, tracking_no, destroy_qty, returned_at, destroy_fee, currency, accounted_at, imported_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
		ON CONFLICT (shop_key, sku_id, tracking_no) DO UPDATE SET
			tracking_type=EXCLUDED.tracking_type, destroy_qty=EXCLUDED.destroy_qty, returned_at=EXCLUDED.returned_at,
			destroy_fee=EXCLUDED.destroy_fee, currency=EXCLUDED.currency, accounted_at=EXCLUDED.accounted_at, imported_at=now()
		RETURNING (xmax = 0)
	`)
	if err != nil {
		return stat, err
	}
	defer stmt.Close()
	for _, item := range items {
		inserted, err := scanInserted(stmt.QueryRowContext(ctx, coalesceShop(shopKey, item.ShopKey), item.SKUID,
			item.TrackingType, item.TrackingNo, item.DestroyQty, nullableTimePtr(item.ReturnedAt),
			item.DestroyFee, item.Currency, nullableTimePtr(item.AccountedAt)))
		if err != nil {
			return stat, fmt.Errorf("处置费 %s: %w", item.TrackingNo, err)
		}
		tally(&stat, inserted)
	}
	return stat, nil
}

func upsertSettledFlows(ctx context.Context, tx *sql.Tx, shopKey string, items []TemuProfitSettledFlow) (ProfitTableStat, error) {
	stat := ProfitTableStat{Table: "temu_profit_settled_flows", Label: "结算流水", Rows: len(items)}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO temu_profit_settled_flows(
			shop_key, flow_id, batch_no, po_no, document_no, trade_type, settle_amount,
			sku_id, sku_name, sku_ext_code, quantity, declared_price, is_activity_price, currency,
			total_declared_price, sku_coupon_amount, shop_coupon_amount, declared_discount_amount,
			activity_freight_discount_amount, received_at, remark, imported_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,now())
		ON CONFLICT (shop_key, flow_id) DO UPDATE SET
			batch_no=EXCLUDED.batch_no, po_no=EXCLUDED.po_no, document_no=EXCLUDED.document_no,
			trade_type=EXCLUDED.trade_type, settle_amount=EXCLUDED.settle_amount, sku_id=EXCLUDED.sku_id,
			sku_name=EXCLUDED.sku_name, sku_ext_code=EXCLUDED.sku_ext_code, quantity=EXCLUDED.quantity,
			declared_price=EXCLUDED.declared_price, is_activity_price=EXCLUDED.is_activity_price,
			currency=EXCLUDED.currency, total_declared_price=EXCLUDED.total_declared_price,
			sku_coupon_amount=EXCLUDED.sku_coupon_amount, shop_coupon_amount=EXCLUDED.shop_coupon_amount,
			declared_discount_amount=EXCLUDED.declared_discount_amount,
			activity_freight_discount_amount=EXCLUDED.activity_freight_discount_amount,
			received_at=EXCLUDED.received_at, remark=EXCLUDED.remark, imported_at=now()
		RETURNING (xmax = 0)
	`)
	if err != nil {
		return stat, err
	}
	defer stmt.Close()
	for _, item := range items {
		inserted, err := scanInserted(stmt.QueryRowContext(ctx, coalesceShop(shopKey, item.ShopKey), item.FlowID, item.BatchNo,
			item.PONo, item.DocumentNo, item.TradeType, item.SettleAmount, nullableInt64(item.SKUID),
			item.SKUName, item.SKUExtCode, nullableFloat64(item.Quantity), nullableFloat64(item.DeclaredPrice),
			nullableBool(item.IsActivityPrice), item.Currency, nullableFloat64(item.TotalDeclaredPrice),
			item.SKUCouponAmount, item.ShopCouponAmount, item.DeclaredDiscountAmount,
			item.ActivityFreightDiscountAmount, nullableTimePtr(item.ReceivedAt), item.Remark))
		if err != nil {
			return stat, fmt.Errorf("结算流水 %s: %w", item.FlowID, err)
		}
		tally(&stat, inserted)
	}
	return stat, nil
}

func upsertSettledParentOrders(ctx context.Context, tx *sql.Tx, shopKey string, items []TemuProfitSettledParentOrder) (ProfitTableStat, error) {
	stat := ProfitTableStat{Table: "temu_profit_settled_parent_orders", Label: "已到账聚合账单", Rows: len(items)}
	orderStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO temu_profit_settled_parent_orders(
			shop_key, po_no, currency, sales_receipt, sales_receipt_after_discount, sales_chargeback,
			freight_receipt, freight_receipt_after_discount, freight_chargeback, imported_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
		ON CONFLICT (shop_key, po_no) DO UPDATE SET
			currency=EXCLUDED.currency, sales_receipt=EXCLUDED.sales_receipt,
			sales_receipt_after_discount=EXCLUDED.sales_receipt_after_discount,
			sales_chargeback=EXCLUDED.sales_chargeback, freight_receipt=EXCLUDED.freight_receipt,
			freight_receipt_after_discount=EXCLUDED.freight_receipt_after_discount,
			freight_chargeback=EXCLUDED.freight_chargeback, imported_at=now()
		RETURNING id, (xmax = 0)
	`)
	if err != nil {
		return stat, err
	}
	defer orderStmt.Close()
	skuStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO temu_profit_settled_parent_skus(
			parent_order_id, line_no, sku_id, sku_name, sku_ext_code, quantity, declared_price, is_activity_price, imported_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
	`)
	if err != nil {
		return stat, err
	}
	defer skuStmt.Close()
	for _, item := range items {
		var id int64
		var inserted bool
		err := orderStmt.QueryRowContext(ctx, coalesceShop(shopKey, item.ShopKey), item.PONo, item.Currency,
			item.SalesReceipt, item.SalesReceiptAfterDiscount, item.SalesChargeback,
			item.FreightReceipt, item.FreightReceiptAfterDiscount, item.FreightChargeback).Scan(&id, &inserted)
		if err != nil {
			return stat, fmt.Errorf("聚合账单 %s: %w", item.PONo, err)
		}
		tally(&stat, inserted)
		if _, err := tx.ExecContext(ctx, `DELETE FROM temu_profit_settled_parent_skus WHERE parent_order_id=$1`, id); err != nil {
			return stat, err
		}
		for _, sku := range item.SKUs {
			if _, err := skuStmt.ExecContext(ctx, id, sku.LineNo, sku.SKUID, sku.SKUName, sku.SKUExtCode,
				sku.Quantity, sku.DeclaredPrice, nullableBool(sku.IsActivityPrice)); err != nil {
				return stat, fmt.Errorf("聚合账单 SKU %s/%d: %w", item.PONo, sku.SKUID, err)
			}
		}
	}
	return stat, nil
}

func upsertUnsettleOrders(ctx context.Context, tx *sql.Tx, shopKey string, items []TemuProfitUnsettleOrder) (ProfitTableStat, error) {
	stat := ProfitTableStat{Table: "temu_profit_unsettle_orders", Label: "待处理款项", Rows: len(items)}
	orderStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO temu_profit_unsettle_orders(
			shop_key, po_no, currency, sales_receipt, sales_receipt_after_discount, sales_chargeback,
			freight_receipt, freight_chargeback, freight_receipt_after_discount, imported_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
		ON CONFLICT (shop_key, po_no) DO UPDATE SET
			currency=EXCLUDED.currency, sales_receipt=EXCLUDED.sales_receipt,
			sales_receipt_after_discount=EXCLUDED.sales_receipt_after_discount,
			sales_chargeback=EXCLUDED.sales_chargeback, freight_receipt=EXCLUDED.freight_receipt,
			freight_chargeback=EXCLUDED.freight_chargeback,
			freight_receipt_after_discount=EXCLUDED.freight_receipt_after_discount, imported_at=now()
		RETURNING id, (xmax = 0)
	`)
	if err != nil {
		return stat, err
	}
	defer orderStmt.Close()
	skuStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO temu_profit_unsettle_skus(
			unsettle_order_id, line_no, sku_id, sku_name, quantity, declared_price, imported_at
		) VALUES ($1,$2,$3,$4,$5,$6,now())
	`)
	if err != nil {
		return stat, err
	}
	defer skuStmt.Close()
	for _, item := range items {
		var id int64
		var inserted bool
		err := orderStmt.QueryRowContext(ctx, coalesceShop(shopKey, item.ShopKey), item.PONo, item.Currency,
			item.SalesReceipt, item.SalesReceiptAfterDiscount, item.SalesChargeback,
			item.FreightReceipt, item.FreightChargeback, item.FreightReceiptAfterDiscount).Scan(&id, &inserted)
		if err != nil {
			return stat, fmt.Errorf("待处理款项 %s: %w", item.PONo, err)
		}
		tally(&stat, inserted)
		if _, err := tx.ExecContext(ctx, `DELETE FROM temu_profit_unsettle_skus WHERE unsettle_order_id=$1`, id); err != nil {
			return stat, err
		}
		for _, sku := range item.SKUs {
			if _, err := skuStmt.ExecContext(ctx, id, sku.LineNo, sku.SKUID, sku.SKUName, sku.Quantity, sku.DeclaredPrice); err != nil {
				return stat, fmt.Errorf("待处理款项 SKU %s/%d: %w", item.PONo, sku.SKUID, err)
			}
		}
	}
	return stat, nil
}

func scanInserted(row *sql.Row) (bool, error) {
	var inserted bool
	err := row.Scan(&inserted)
	return inserted, err
}

func tally(stat *ProfitTableStat, inserted bool) {
	if inserted {
		stat.Inserted++
	} else {
		stat.Updated++
	}
}

func coalesceShop(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func nullableTimePtr(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}
