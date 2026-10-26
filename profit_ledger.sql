-- Persist identifiers derived from raw API snapshots once, rather than
-- decompressing every historical JSON document for each report request.
ALTER TABLE normalized_order_lines ADD COLUMN IF NOT EXISTS platform_sku_id bigint GENERATED ALWAYS AS
 (CASE WHEN raw_payload->>'skuId' ~ '^[0-9]{1,18}$' THEN (raw_payload->>'skuId')::bigint END) STORED;
ALTER TABLE normalized_orders ADD COLUMN IF NOT EXISTS source_item_count integer GENERATED ALWAYS AS
 (CASE WHEN jsonb_typeof(raw_payload->'orderList')='array' THEN jsonb_array_length(raw_payload->'orderList') END) STORED;
CREATE TABLE IF NOT EXISTS profit_order_packages (
 shop_key text NOT NULL,source text NOT NULL,source_key text NOT NULL,
 po_no text NOT NULL CHECK(po_no<>''),child_order_no text NOT NULL DEFAULT '',package_no text NOT NULL CHECK(package_no<>''),
 tracking_no text NOT NULL DEFAULT '',source_updated_at timestamptz NOT NULL,
 PRIMARY KEY(shop_key,source,source_key,po_no,child_order_no,package_no,tracking_no)
);
CREATE INDEX IF NOT EXISTS profit_packages_package_idx ON profit_order_packages(shop_key,package_no);
CREATE INDEX IF NOT EXISTS profit_packages_tracking_idx ON profit_order_packages(shop_key,tracking_no) WHERE tracking_no<>'';
CREATE INDEX IF NOT EXISTS profit_packages_po_idx ON profit_order_packages(shop_key,po_no);
CREATE TABLE IF NOT EXISTS profit_package_sync (
 shop_key text PRIMARY KEY,attempted_at timestamptz NOT NULL,succeeded_at timestamptz,
 link_count integer NOT NULL DEFAULT 0,error_message text NOT NULL DEFAULT ''
);

-- One entry per original posted transaction. Parent summaries and pending
-- estimates are deliberately excluded from the cash ledger.
CREATE OR REPLACE VIEW profit_ledger_source AS
 SELECT 'settlement'::text source,id source_id,shop_key,flow_id source_reference,trade_type fee_name,po_no order_reference,
 ''::text package_no,''::text tracking_no,sku_id,sku_ext_code source_sku,currency,settle_amount original_amount,settle_amount amount,received_at occurred_at,received_at paid_at,remark
 FROM temu_profit_settled_flows
 UNION ALL
 SELECT 'shipping',id,shop_key,id::text,'发货面单费','',package_no,tracking_no,NULL::bigint,'',currency,freight_amount,freight_amount,occurred_at,NULL::timestamptz,remark
 FROM temu_profit_shipping_label_fees WHERE billing_phase='posted'
 UNION ALL
 SELECT 'return_merchant',id,shop_key,fund_bill_id,'退货面单费·商家仓',po_no,'',tracking_no,NULL::bigint,'',currency,amount,amount,occurred_at,NULL::timestamptz,bill_remark
 FROM temu_profit_return_label_fees WHERE return_destination='merchant_warehouse'
 UNION ALL
 SELECT 'return_third_party',id,shop_key,fund_bill_id,'退货面单费·第三方仓',po_no,'',tracking_no,NULL::bigint,'',currency,amount,amount,occurred_at,NULL::timestamptz,bill_remark
 FROM temu_profit_return_label_fees WHERE return_destination='third_party_warehouse'
 UNION ALL
 SELECT 'buyer_chargeback',id,shop_key,violation_order_no,'买家拒付',violation_order_no,'','',NULL::bigint,'',currency,amount,-ABS(amount),accounted_at,NULL::timestamptz,''
 FROM temu_profit_buyer_chargebacks
 UNION ALL
 SELECT source_sheet,id,shop_key,violation_no,CASE source_sheet WHEN 'fulfillment_delay' THEN '履约违规·延迟到货' ELSE '履约违规·虚假发货' END,order_no,'','',NULL::bigint,'',currency,amount,-ABS(amount),accounted_at,NULL::timestamptz,violation_type
 FROM temu_profit_fulfillment_violations
 UNION ALL
 SELECT 'platform_return',id,shop_key,order_no,'退货面单费平台承担',order_no,'','',NULL::bigint,'',currency,amount,amount,accounted_at,NULL::timestamptz,remark
 FROM temu_profit_platform_return_label_fees
 UNION ALL
 SELECT 'disposal',id,shop_key,id::text,'处置费','','',tracking_no,sku_id,'',currency,destroy_fee,-ABS(destroy_fee),accounted_at,NULL::timestamptz,''
 FROM temu_profit_disposal_fees;

CREATE OR REPLACE VIEW profit_ledger_identity AS
 WITH identities AS (
 SELECT o.shop_key,l.platform_product_sku_id sku_id,l.platform_sku FROM normalized_orders o JOIN normalized_order_lines l ON l.order_id=o.id WHERE o.platform='temu'
 UNION ALL
 SELECT o.shop_key,l.platform_sku_id,l.platform_sku
 FROM normalized_orders o JOIN normalized_order_lines l ON l.order_id=o.id WHERE o.platform='temu'
 UNION ALL
 SELECT shop_key,sku_id,sku_ext_code FROM temu_profit_settled_flows
 UNION ALL
 SELECT p.shop_key,k.sku_id,k.sku_ext_code FROM temu_profit_settled_parent_orders p JOIN temu_profit_settled_parent_skus k ON k.parent_order_id=p.id
 )
 SELECT shop_key,sku_id,COUNT(DISTINCT platform_sku) candidates,MIN(platform_sku) platform_sku
 FROM identities WHERE sku_id IS NOT NULL AND platform_sku<>'' GROUP BY 1,2;

CREATE OR REPLACE VIEW profit_ledger_products AS
 WITH identity_map AS MATERIALIZED(SELECT * FROM profit_ledger_identity), evidence AS (
 SELECT o.shop_key,o.source_order_no po_no,l.platform_sku,l.product_name,l.variant_name,l.platform_product_sku_id sku_id
 FROM normalized_orders o JOIN normalized_order_lines l ON l.order_id=o.id WHERE o.platform='temu'
 UNION ALL
 SELECT p.shop_key,p.po_no,COALESCE(NULLIF(k.sku_ext_code,''),CASE WHEN m.candidates=1 THEN m.platform_sku END,''),k.sku_name,'',k.sku_id
 FROM temu_profit_settled_parent_orders p JOIN temu_profit_settled_parent_skus k ON k.parent_order_id=p.id LEFT JOIN identity_map m ON m.shop_key=p.shop_key AND m.sku_id=k.sku_id
 UNION ALL
 SELECT f.shop_key,f.po_no,COALESCE(NULLIF(f.sku_ext_code,''),CASE WHEN m.candidates=1 THEN m.platform_sku END,''),f.sku_name,'',f.sku_id
 FROM temu_profit_settled_flows f LEFT JOIN identity_map m USING(shop_key,sku_id) WHERE f.sku_ext_code<>'' OR f.sku_id IS NOT NULL
 ), items AS (
 SELECT shop_key,po_no,platform_sku,MAX(product_name) product_name,MAX(variant_name) variant_name,MIN(sku_id) sku_id
 FROM evidence WHERE po_no<>'' GROUP BY 1,2,3
 )
 , quality AS MATERIALIZED(
 SELECT o.shop_key,o.source_order_no po_no,o.source_item_count>COUNT(l.id) incomplete
 FROM normalized_orders o LEFT JOIN normalized_order_lines l ON l.order_id=o.id
 WHERE o.platform='temu' GROUP BY o.id
 )
 SELECT items.shop_key,items.po_no,COUNT(*) FILTER(WHERE platform_sku<>'') candidate_skus,
 BOOL_OR(platform_sku='') OR COALESCE(BOOL_OR(q.incomplete),false) incomplete,
 MIN(NULLIF(platform_sku,'')) platform_sku,
 jsonb_agg(jsonb_build_object('platform_sku',platform_sku,'product_name',product_name,'variant_name',variant_name,'sku_id',sku_id) ORDER BY platform_sku) products
 FROM items LEFT JOIN quality q USING(shop_key,po_no) GROUP BY 1,2;

CREATE OR REPLACE VIEW profit_ledger AS
 WITH identity_map AS MATERIALIZED(SELECT * FROM profit_ledger_identity),
 products_map AS MATERIALIZED(SELECT * FROM profit_ledger_products),
 attribution AS MATERIALIZED(SELECT * FROM profit_settlement_attribution),
 package_tokens AS MATERIALIZED (SELECT shop_key,package_no value,array_agg(DISTINCT po_no ORDER BY po_no) pos FROM profit_order_packages GROUP BY 1,2),
 tracking_tokens AS MATERIALIZED (SELECT shop_key,tracking_no value,array_agg(DISTINCT po_no ORDER BY po_no) pos FROM profit_order_packages WHERE tracking_no<>'' GROUP BY 1,2), children AS MATERIALIZED (
 SELECT o.shop_key,l.source_line_key child,array_agg(DISTINCT o.source_order_no ORDER BY o.source_order_no) pos
 FROM normalized_orders o JOIN normalized_order_lines l ON l.order_id=o.id WHERE o.platform='temu' GROUP BY 1,2
 ), resolved AS (
 SELECT f.*,CASE WHEN cardinality(c.pos)=1 THEN c.pos[1] ELSE f.order_reference END ref,
 COALESCE(c.pos,'{}'::text[]) child_pos,COALESCE(pk.pos,'{}'::text[]) package_pos,COALESCE(tr.pos,'{}'::text[]) tracking_pos
 FROM profit_ledger_source f
 LEFT JOIN children c ON c.shop_key=f.shop_key AND c.child=f.order_reference AND f.source<>'settlement'
 LEFT JOIN package_tokens pk ON pk.shop_key=f.shop_key AND pk.value=f.package_no AND f.package_no<>''
 LEFT JOIN tracking_tokens tr ON tr.shop_key=f.shop_key AND tr.value=f.tracking_no AND f.tracking_no<>''
 ), candidates AS (
 SELECT r.*,CASE WHEN cardinality(child_pos)>1 THEN child_pos WHEN ref<>'' THEN ARRAY[ref] WHEN cardinality(package_pos)>0 THEN package_pos ELSE tracking_pos END candidate_pos,
 (cardinality(package_pos)>0 AND cardinality(tracking_pos)>0 AND package_pos<>tracking_pos)
 OR (ref<>'' AND cardinality(package_pos)>0 AND NOT(ref=ANY(package_pos)))
 OR (ref<>'' AND cardinality(tracking_pos)>0 AND NOT(ref=ANY(tracking_pos))) link_conflict
 FROM resolved r
 ), matched AS (
 SELECT c.*,CASE WHEN NOT link_conflict AND cardinality(candidate_pos)=1 THEN candidate_pos[1] ELSE '' END po_no
 FROM candidates c
 ), product_match AS (
 SELECT f.*,COALESCE(p.candidate_skus,0) candidate_skus,COALESCE(p.products,'[]'::jsonb) products,
 CASE WHEN f.source='settlement' THEN a.platform_sku
 WHEN link_conflict THEN '' WHEN f.source_sku<>'' THEN f.source_sku
 WHEN f.sku_id IS NOT NULL AND m.candidates=1 THEN m.platform_sku
 WHEN f.sku_id IS NULL AND p.candidate_skus=1 AND NOT p.incomplete AND cardinality(candidate_pos)=1 THEN p.platform_sku ELSE '' END platform_sku,
 CASE WHEN f.source='settlement' THEN a.reason WHEN link_conflict THEN '包裹、运单或订单关联冲突'
 WHEN f.sku_id IS NOT NULL AND COALESCE(m.candidates,0)<>1 THEN 'SKU ID 未找到唯一编码'
 WHEN cardinality(candidate_pos)>1 THEN '合并发货涉及多个 PO，待分摊'
 WHEN COALESCE(p.incomplete,false) THEN 'PO 商品编码不完整'
 WHEN p.candidate_skus>1 THEN '多 SKU PO 待分摊' ELSE '未找到订单商品' END unresolved_reason
 FROM matched f LEFT JOIN products_map p ON p.shop_key=f.shop_key AND p.po_no=f.po_no
 LEFT JOIN identity_map m ON m.shop_key=f.shop_key AND m.sku_id=f.sku_id
 LEFT JOIN attribution a ON f.source='settlement' AND a.id=f.source_id
 )
 SELECT f.source||':'||f.source_id entry_key,f.source,f.source_id,f.shop_key,f.source_reference,f.fee_name,f.order_reference,f.po_no,
 f.package_no,f.tracking_no,f.sku_id,f.source_sku,f.currency,f.original_amount,f.amount,f.occurred_at,f.paid_at,f.remark,
 COALESCE(f.platform_sku,'') platform_sku,f.candidate_pos,f.package_pos,f.tracking_pos,f.candidate_skus,f.products,
 CASE WHEN link_conflict THEN 'conflict' WHEN cardinality(candidate_pos)>1 THEN 'ambiguous'
 WHEN po_no<>'' AND (o.id IS NOT NULL OR candidate_skus>0 OR cardinality(package_pos)>0 OR cardinality(tracking_pos)>0) THEN 'matched' ELSE 'unmatched' END order_status,
 CASE WHEN COALESCE(f.platform_sku,'')<>'' THEN 'matched' ELSE 'unattributed' END sku_status,
 CASE WHEN COALESCE(f.platform_sku,'')='' THEN unresolved_reason ELSE '' END reason,
 o.occurred_at order_at,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('source',x.source,'source_key',x.source_key,'child_order_no',x.child_order_no,'package_no',x.package_no,'tracking_no',x.tracking_no,'source_updated_at',x.source_updated_at) ORDER BY x.package_no,x.tracking_no,x.source,x.source_key,x.child_order_no) FROM profit_order_packages x WHERE x.shop_key=f.shop_key AND x.po_no=ANY(f.candidate_pos)),'[]'::jsonb) package_evidence
 FROM product_match f LEFT JOIN normalized_orders o ON o.platform='temu' AND o.shop_key=f.shop_key AND o.source_order_no=f.po_no;
