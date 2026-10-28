-- Independent, immutable source evidence. None of these amounts enter revenue.
CREATE TABLE IF NOT EXISTS profit_bill_files (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 shop_key text NOT NULL, source text NOT NULL CHECK(source IN ('fund_detail','settled_flow','settled_parent')),
 filename text NOT NULL, sha256 text NOT NULL, hash_basis text NOT NULL CHECK(hash_basis IN ('file','parsed_rows')),
 imported_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(shop_key,source,sha256,hash_basis)
);
CREATE TABLE IF NOT EXISTS profit_bill_rows (
 file_id bigint NOT NULL REFERENCES profit_bill_files(id), row_no integer NOT NULL,
 business_key text NOT NULL, po_no text NOT NULL, currency text NOT NULL, trade_type text NOT NULL,
 amount numeric, sales_receipt numeric, freight_receipt numeric, sales_chargeback numeric, freight_chargeback numeric,
 discounted boolean NOT NULL, direction_error boolean NOT NULL, received_at timestamptz, payload jsonb NOT NULL,
 PRIMARY KEY(file_id,row_no), UNIQUE(file_id,business_key)
);
CREATE INDEX IF NOT EXISTS profit_bill_rows_po_idx ON profit_bill_rows(po_no,currency);
CREATE OR REPLACE VIEW profit_bill_current_rows AS
 SELECT DISTINCT ON(f.shop_key,f.source,r.business_key) f.shop_key,f.source,f.filename,f.sha256,f.hash_basis,f.imported_at,r.*
 FROM profit_bill_files f JOIN profit_bill_rows r ON r.file_id=f.id
 ORDER BY f.shop_key,f.source,r.business_key,f.id DESC;

CREATE OR REPLACE VIEW profit_bill_comparison AS
WITH current_rows AS MATERIALIZED (SELECT * FROM profit_bill_current_rows),
 grouped AS (
 SELECT shop_key,po_no,currency,source,COUNT(*) AS rows,
  CASE WHEN source='settled_parent' THEN SUM(sales_receipt) ELSE COALESCE(SUM(amount) FILTER(WHERE trade_type='销售回款'),0) END AS sales_receipt,
  CASE WHEN source='settled_parent' THEN SUM(freight_receipt) ELSE COALESCE(SUM(amount) FILTER(WHERE trade_type='运费回款'),0) END AS freight_receipt,
  CASE WHEN source='settled_parent' THEN -SUM(ABS(sales_chargeback)) ELSE COALESCE(SUM(amount) FILTER(WHERE trade_type='销售冲回'),0) END AS sales_chargeback,
  CASE WHEN source='settled_parent' THEN -SUM(ABS(freight_chargeback)) ELSE COALESCE(SUM(amount) FILTER(WHERE trade_type='运费冲回'),0) END AS freight_chargeback,
  CASE WHEN source='settled_parent' THEN SUM(ROUND(sales_receipt,4)) ELSE COALESCE(SUM(ROUND(amount,4)) FILTER(WHERE trade_type='销售回款'),0) END AS accounting_sales_receipt,
  CASE WHEN source='settled_parent' THEN SUM(ROUND(freight_receipt,4)) ELSE COALESCE(SUM(ROUND(amount,4)) FILTER(WHERE trade_type='运费回款'),0) END AS accounting_freight_receipt,
  CASE WHEN source='settled_parent' THEN -SUM(ABS(ROUND(sales_chargeback,4))) ELSE COALESCE(SUM(ROUND(amount,4)) FILTER(WHERE trade_type='销售冲回'),0) END AS accounting_sales_chargeback,
  CASE WHEN source='settled_parent' THEN -SUM(ABS(ROUND(freight_chargeback,4))) ELSE COALESCE(SUM(ROUND(amount,4)) FILTER(WHERE trade_type='运费冲回'),0) END AS accounting_freight_chargeback,
  COUNT(*) FILTER(WHERE source<>'settled_parent' AND trade_type NOT IN ('销售回款','运费回款','销售冲回','运费冲回')) AS unknown_rows,
  BOOL_OR(discounted) AS discounted,BOOL_OR(direction_error OR (trade_type IN ('销售回款','运费回款') AND amount<0) OR (trade_type IN ('销售冲回','运费冲回') AND amount>0) OR sales_receipt<0 OR freight_receipt<0) AS direction_error,
  BOOL_OR(source='settled_parent' AND (sales_receipt IS NULL OR freight_receipt IS NULL OR sales_chargeback IS NULL OR freight_chargeback IS NULL)) AS incomplete,
  MIN(received_at) AS first_at,MAX(received_at) AS last_at,
  jsonb_agg(DISTINCT jsonb_build_object('id',file_id,'filename',filename,'sha256',sha256,'hash_basis',hash_basis)) AS files
 FROM current_rows GROUP BY 1,2,3,4
), pairs AS (
 SELECT f.shop_key AS fs,f.po_no AS fp,f.currency AS fc,d.shop_key AS ds,d.po_no AS dp,d.currency AS dc,
  f.business_key AS fk,d.business_key AS dk,
  f.po_no IS DISTINCT FROM d.po_no OR f.currency IS DISTINCT FROM d.currency OR f.trade_type IS DISTINCT FROM d.trade_type OR f.amount IS DISTINCT FROM d.amount AS differs,
  f.po_no IS DISTINCT FROM d.po_no OR f.currency IS DISTINCT FROM d.currency OR f.trade_type IS DISTINCT FROM d.trade_type OR ROUND(f.amount,4) IS DISTINCT FROM ROUND(d.amount,4) AS accounting_differs
 FROM (SELECT * FROM current_rows WHERE source='fund_detail') f
 FULL JOIN (SELECT * FROM current_rows WHERE source='settled_flow') d ON f.shop_key=d.shop_key AND f.business_key=d.business_key
), flow_issues AS (
 SELECT shop_key,po_no,currency,COUNT(*) AS flow_differences,COUNT(*) FILTER(WHERE accounting_differs) AS accounting_flow_differences FROM (
  SELECT fs AS shop_key,fp AS po_no,fc AS currency,fk AS business_key,accounting_differs FROM pairs WHERE differs AND fs IS NOT NULL
  UNION SELECT ds,dp,dc,dk,accounting_differs FROM pairs WHERE differs AND ds IS NOT NULL
 ) v GROUP BY 1,2,3
), keys AS (
 SELECT shop_key,po_no,currency FROM grouped
 UNION SELECT shop_key,po_no,currency FROM temu_profit_settled_flows
 UNION SELECT shop_key,po_no,currency FROM temu_profit_settled_parent_orders
), currencies AS (
 SELECT shop_key,po_no,COUNT(DISTINCT currency) FILTER(WHERE currency<>'') AS currencies FROM keys GROUP BY 1,2
), compared AS (
 SELECT k.*,COALESCE(x.flow_differences,0) AS flow_differences,COALESCE(x.accounting_flow_differences,0) AS accounting_flow_differences,
  CASE WHEN f.rows IS NULL THEN NULL ELSE jsonb_build_object('rows',f.rows,'sales_receipt',f.sales_receipt::text,'freight_receipt',f.freight_receipt::text,'sales_chargeback',f.sales_chargeback::text,'freight_chargeback',f.freight_chargeback::text,'first_at',f.first_at,'last_at',f.last_at,'files',f.files) END AS fund,
  CASE WHEN d.rows IS NULL THEN NULL ELSE jsonb_build_object('rows',d.rows,'sales_receipt',d.sales_receipt::text,'freight_receipt',d.freight_receipt::text,'sales_chargeback',d.sales_chargeback::text,'freight_chargeback',d.freight_chargeback::text,'first_at',d.first_at,'last_at',d.last_at,'files',d.files) END AS detail,
  CASE WHEN p.rows IS NULL THEN NULL ELSE jsonb_build_object('rows',p.rows,'sales_receipt',p.sales_receipt::text,'freight_receipt',p.freight_receipt::text,'sales_chargeback',p.sales_chargeback::text,'freight_chargeback',p.freight_chargeback::text,'files',p.files) END AS parent,
  jsonb_build_object('fund_detail',jsonb_build_object('sales_receipt',(f.sales_receipt-d.sales_receipt)::text,'freight_receipt',(f.freight_receipt-d.freight_receipt)::text,'sales_chargeback',(f.sales_chargeback-d.sales_chargeback)::text,'freight_chargeback',(f.freight_chargeback-d.freight_chargeback)::text),
    'detail_parent',jsonb_build_object('sales_receipt',(d.sales_receipt-p.sales_receipt)::text,'freight_receipt',(d.freight_receipt-p.freight_receipt)::text,'sales_chargeback',(d.sales_chargeback-p.sales_chargeback)::text,'freight_chargeback',(d.freight_chargeback-p.freight_chargeback)::text)) AS deltas,
  ARRAY_REMOVE(ARRAY[
   CASE WHEN k.po_no='' THEN 'PO 单号缺失' END,CASE WHEN k.currency='' THEN '币种缺失' END,
   CASE WHEN c.currencies>1 THEN '同一 PO 存在多个币种' END,
   CASE WHEN COALESCE(f.unknown_rows,0)+COALESCE(d.unknown_rows,0)>0 THEN '含未识别的结算交易类型' END,
   CASE WHEN f.discounted OR d.discounted OR p.discounted THEN '折后回款口径待确认' END,
   CASE WHEN f.direction_error OR d.direction_error OR p.direction_error THEN '金额方向异常' END
   ,CASE WHEN p.incomplete THEN '聚合款项字段缺失' END
  ],NULL) AS review_reasons,
  ARRAY_REMOVE(ARRAY[CASE WHEN f.rows IS NULL THEN '缺少账务明细结算依据' END,CASE WHEN d.rows IS NULL THEN '缺少 PO 明细依据' END,CASE WHEN p.rows IS NULL THEN '缺少 PO 聚合依据' END],NULL) AS missing_reasons,
  COALESCE((f.sales_receipt,f.freight_receipt,f.sales_chargeback,f.freight_chargeback)<>(d.sales_receipt,d.freight_receipt,d.sales_chargeback,d.freight_chargeback),false)
  OR COALESCE((d.sales_receipt,d.freight_receipt,d.sales_chargeback,d.freight_chargeback)<>(p.sales_receipt,p.freight_receipt,p.sales_chargeback,p.freight_chargeback),false)
  OR COALESCE((f.sales_receipt,f.freight_receipt,f.sales_chargeback,f.freight_chargeback)<>(p.sales_receipt,p.freight_receipt,p.sales_chargeback,p.freight_chargeback),false) AS amount_differs,
  COALESCE((f.accounting_sales_receipt,f.accounting_freight_receipt,f.accounting_sales_chargeback,f.accounting_freight_chargeback)<>(d.accounting_sales_receipt,d.accounting_freight_receipt,d.accounting_sales_chargeback,d.accounting_freight_chargeback),false) OR COALESCE((d.accounting_sales_receipt,d.accounting_freight_receipt,d.accounting_sales_chargeback,d.accounting_freight_chargeback)<>(p.accounting_sales_receipt,p.accounting_freight_receipt,p.accounting_sales_chargeback,p.accounting_freight_chargeback),false) OR COALESCE((f.accounting_sales_receipt,f.accounting_freight_receipt,f.accounting_sales_chargeback,f.accounting_freight_chargeback)<>(p.accounting_sales_receipt,p.accounting_freight_receipt,p.accounting_sales_chargeback,p.accounting_freight_chargeback),false) AS accounting_amount_differs
 FROM keys k JOIN currencies c USING(shop_key,po_no)
 LEFT JOIN grouped f ON (f.shop_key,f.po_no,f.currency,f.source)=(k.shop_key,k.po_no,k.currency,'fund_detail')
 LEFT JOIN grouped d ON (d.shop_key,d.po_no,d.currency,d.source)=(k.shop_key,k.po_no,k.currency,'settled_flow')
 LEFT JOIN grouped p ON (p.shop_key,p.po_no,p.currency,p.source)=(k.shop_key,k.po_no,k.currency,'settled_parent')
 LEFT JOIN flow_issues x ON (x.shop_key,x.po_no,x.currency)=(k.shop_key,k.po_no,k.currency)
)
SELECT shop_key,po_no,currency,fund,detail,parent,deltas,flow_differences,
 CASE WHEN cardinality(review_reasons)>0 THEN 'review' WHEN cardinality(missing_reasons)>0 THEN 'missing' WHEN accounting_amount_differs OR accounting_flow_differences>0 THEN 'difference' WHEN amount_differs OR flow_differences>0 THEN 'precision' ELSE 'matched' END AS status,
 review_reasons||missing_reasons||CASE WHEN amount_differs THEN ARRAY['四类款项金额不一致'] ELSE ARRAY[]::text[] END||CASE WHEN flow_differences>0 THEN ARRAY['结算流水缺少或金额 / 关联不一致'] ELSE ARRAY[]::text[] END||CASE WHEN NOT accounting_amount_differs AND accounting_flow_differences=0 AND (amount_differs OR flow_differences>0) THEN ARRAY['按逐笔四位小数核对一致，原始小数存在差异'] ELSE ARRAY[]::text[] END AS reasons,accounting_flow_differences
FROM compared;
