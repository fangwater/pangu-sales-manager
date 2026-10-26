-- Imported values remain intact. Full snapshots archive absent rows; reconciliation
-- is calculated from posted transactions and never deletes the pending evidence.
ALTER TABLE temu_profit_shipping_label_fees ADD COLUMN IF NOT EXISTS pending_active boolean NOT NULL DEFAULT true;
ALTER TABLE temu_profit_unsettle_orders ADD COLUMN IF NOT EXISTS pending_active boolean NOT NULL DEFAULT true;

CREATE OR REPLACE VIEW profit_pending_shipping_reconciliation AS
WITH posted AS (
 SELECT shop_key,package_no,tracking_no,bill_type,currency,SIGN(freight_amount) AS direction,
        SUM(ABS(freight_amount)) AS posted_amount,COUNT(*) AS posted_rows,MAX(occurred_at) AS posted_at
 FROM temu_profit_shipping_label_fees
 WHERE billing_phase='posted' AND reconcile_status IN ('','支出成功','退款成功','已出账','已结算')
 GROUP BY 1,2,3,4,5,6
), pending AS (
 SELECT MIN(id) AS id,shop_key,package_no,tracking_no,bill_type,currency,pending_active AS snapshot_active,
        SIGN(freight_amount) AS direction,SUM(freight_amount) AS raw_amount,COUNT(*) AS source_rows,
        jsonb_agg(id ORDER BY id) AS source_ids,MAX(imported_at) AS imported_at,
        STRING_AGG(DISTINCT remark,' / ' ORDER BY remark) AS remarks
 FROM temu_profit_shipping_label_fees WHERE billing_phase='pending'
 GROUP BY shop_key,package_no,tracking_no,bill_type,currency,pending_active,SIGN(freight_amount)
), matched AS (
 SELECT u.*,COALESCE(p.posted_amount,0) AS posted_amount,COALESCE(p.posted_rows,0) AS posted_rows,p.posted_at,
        CASE WHEN currency='' THEN '币种缺失'
             WHEN package_no='' AND tracking_no='' THEN '包裹号和运单号均缺失' ELSE '' END AS reason,
        CASE WHEN currency='' OR (package_no='' AND tracking_no='') THEN 0::numeric
             ELSE LEAST(ABS(raw_amount),COALESCE(p.posted_amount,0)) END AS covered
 FROM pending u LEFT JOIN posted p USING(shop_key,package_no,tracking_no,bill_type,currency,direction)
)
SELECT id,shop_key,currency,package_no AS reference,tracking_no AS secondary_reference,bill_type,
       raw_amount,direction*covered AS matched_amount,direction*(ABS(raw_amount)-covered) AS remaining_amount,
       source_rows,source_ids,snapshot_active,imported_at,posted_amount,posted_rows,posted_at,remarks,reason,
       CASE WHEN NOT snapshot_active THEN 'archived' WHEN reason<>'' THEN 'review'
            WHEN ABS(raw_amount)=covered THEN 'settled' WHEN covered>0 THEN 'partial' ELSE 'pending' END AS status
FROM matched;

CREATE OR REPLACE VIEW profit_pending_order_reconciliation AS
WITH flows AS (
 SELECT shop_key,po_no,currency,
        COALESCE(SUM(settle_amount) FILTER(WHERE trade_type='销售回款'),0) AS sales_receipt,
        COALESCE(SUM(settle_amount) FILTER(WHERE trade_type='运费回款'),0) AS freight_receipt,
        COALESCE(-SUM(settle_amount) FILTER(WHERE trade_type='销售冲回'),0) AS sales_chargeback,
        COALESCE(-SUM(settle_amount) FILTER(WHERE trade_type='运费冲回'),0) AS freight_chargeback,
        COUNT(*) FILTER(WHERE trade_type NOT IN ('销售回款','运费回款','销售冲回','运费冲回')) AS unknown_rows,
        COUNT(*) AS posted_rows,MAX(received_at) AS posted_at,0::numeric AS discounted
 FROM temu_profit_settled_flows GROUP BY 1,2,3
), actual AS (
 SELECT * FROM flows
 UNION ALL
 SELECT p.shop_key,p.po_no,p.currency,p.sales_receipt,p.freight_receipt,ABS(p.sales_chargeback),ABS(p.freight_chargeback),
        0::bigint,1::bigint,NULL::timestamptz,ABS(p.sales_receipt_after_discount)+ABS(p.freight_receipt_after_discount)
 FROM temu_profit_settled_parent_orders p
 WHERE NOT EXISTS(SELECT 1 FROM flows f WHERE f.shop_key=p.shop_key AND f.po_no=p.po_no AND f.currency=p.currency)
), matched AS (
 SELECT u.*,COALESCE(a.posted_rows,0) AS posted_rows,a.posted_at,
        CASE WHEN u.currency='' THEN '币种缺失' WHEN u.po_no='' THEN 'PO 单号缺失'
             WHEN u.sales_receipt_after_discount<>0 OR u.freight_receipt_after_discount<>0 OR COALESCE(a.discounted,0)<>0 THEN '折后回款口径待确认'
             WHEN u.sales_receipt<0 OR u.freight_receipt<0 THEN '回款金额方向异常'
             WHEN COALESCE(a.unknown_rows,0)>0 THEN '含未识别的结算交易类型' ELSE '' END AS reason,
        GREATEST(0,u.sales_receipt-GREATEST(0,COALESCE(a.sales_receipt,0))) AS rs,
        GREATEST(0,u.freight_receipt-GREATEST(0,COALESCE(a.freight_receipt,0))) AS rf,
        GREATEST(0,ABS(u.sales_chargeback)-GREATEST(0,COALESCE(a.sales_chargeback,0))) AS rsc,
        GREATEST(0,ABS(u.freight_chargeback)-GREATEST(0,COALESCE(a.freight_chargeback,0))) AS rfc
 FROM temu_profit_unsettle_orders u LEFT JOIN actual a USING(shop_key,po_no,currency)
), balances AS (
 SELECT *,sales_receipt+freight_receipt-ABS(sales_chargeback)-ABS(freight_chargeback) AS raw_amount,
        CASE WHEN reason<>'' THEN sales_receipt ELSE rs END AS remaining_sales_receipt,
        CASE WHEN reason<>'' THEN freight_receipt ELSE rf END AS remaining_freight_receipt,
        CASE WHEN reason<>'' THEN -ABS(sales_chargeback) ELSE -rsc END AS remaining_sales_chargeback,
        CASE WHEN reason<>'' THEN -ABS(freight_chargeback) ELSE -rfc END AS remaining_freight_chargeback
 FROM matched
)
SELECT id,shop_key,currency,po_no AS reference,''::text AS secondary_reference,raw_amount,
       raw_amount-(remaining_sales_receipt+remaining_freight_receipt+remaining_sales_chargeback+remaining_freight_chargeback) AS matched_amount,
       remaining_sales_receipt+remaining_freight_receipt+remaining_sales_chargeback+remaining_freight_chargeback AS remaining_amount,
       1::bigint AS source_rows,pending_active AS snapshot_active,imported_at,posted_rows,posted_at,reason,
       remaining_sales_receipt,remaining_freight_receipt,remaining_sales_chargeback,remaining_freight_chargeback,
       sales_receipt,freight_receipt,sales_chargeback,freight_chargeback,sales_receipt_after_discount,freight_receipt_after_discount,
       CASE WHEN NOT pending_active THEN 'archived' WHEN reason<>'' THEN 'review'
            WHEN posted_rows>0 AND rs+rf+rsc+rfc=0 THEN 'settled'
            WHEN posted_rows>0 AND rs+rf+rsc+rfc<sales_receipt+freight_receipt+ABS(sales_chargeback)+ABS(freight_chargeback) THEN 'partial'
            ELSE 'pending' END AS status
FROM balances;
