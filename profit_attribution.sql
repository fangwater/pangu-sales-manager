-- Identity and PO evidence are kept within each shop. Missing or conflicting
-- product identities remain unallocated rather than receiving an arbitrary split.
CREATE OR REPLACE VIEW profit_settlement_attribution AS
WITH identities AS (
 SELECT o.shop_key,l.platform_product_sku_id sku_id,l.platform_sku
 FROM normalized_orders o JOIN normalized_order_lines l ON l.order_id=o.id
 WHERE o.platform='temu' AND l.platform_product_sku_id IS NOT NULL AND l.platform_sku<>''
 UNION ALL
 SELECT shop_key,sku_id,sku_ext_code FROM temu_profit_settled_flows WHERE sku_id IS NOT NULL AND sku_ext_code<>''
 UNION ALL
 SELECT p.shop_key,k.sku_id,k.sku_ext_code FROM temu_profit_settled_parent_orders p
 JOIN temu_profit_settled_parent_skus k ON k.parent_order_id=p.id WHERE k.sku_ext_code<>''
), sku_map AS (
 SELECT shop_key,sku_id,COUNT(DISTINCT platform_sku) candidates,MIN(platform_sku) platform_sku
 FROM identities GROUP BY 1,2
), evidence AS (
 SELECT o.shop_key,o.source_order_no po_no,COALESCE(NULLIF(l.platform_sku,''),CASE WHEN m.candidates=1 THEN m.platform_sku END,'') platform_sku,l.product_name sku_name
 FROM normalized_orders o JOIN normalized_order_lines l ON l.order_id=o.id
 LEFT JOIN sku_map m ON m.shop_key=o.shop_key AND m.sku_id=l.platform_product_sku_id
 WHERE o.platform='temu'
 UNION ALL
 SELECT f.shop_key,f.po_no,COALESCE(NULLIF(f.sku_ext_code,''),CASE WHEN m.candidates=1 THEN m.platform_sku END,''),f.sku_name
 FROM temu_profit_settled_flows f LEFT JOIN sku_map m USING(shop_key,sku_id)
 WHERE f.sku_ext_code<>'' OR f.sku_id IS NOT NULL
 UNION ALL
 SELECT p.shop_key,p.po_no,COALESCE(NULLIF(k.sku_ext_code,''),CASE WHEN m.candidates=1 THEN m.platform_sku END,''),k.sku_name
 FROM temu_profit_settled_parent_orders p JOIN temu_profit_settled_parent_skus k ON k.parent_order_id=p.id
 LEFT JOIN sku_map m ON m.shop_key=p.shop_key AND m.sku_id=k.sku_id
), orders AS (
 SELECT shop_key,po_no,COUNT(DISTINCT NULLIF(platform_sku,'')) candidates,BOOL_OR(platform_sku='') incomplete,
        MIN(NULLIF(platform_sku,'')) platform_sku,MAX(sku_name) sku_name
 FROM evidence WHERE po_no<>'' GROUP BY 1,2
), matched AS (
 SELECT f.*,COALESCE(p.candidates,0) candidate_skus,COALESCE(p.incomplete,false) incomplete,
        CASE WHEN f.sku_ext_code<>'' THEN f.sku_ext_code
             WHEN f.sku_id IS NOT NULL AND m.candidates=1 THEN m.platform_sku
             WHEN f.sku_id IS NULL AND p.candidates=1 AND NOT p.incomplete THEN p.platform_sku ELSE '' END platform_sku,
        CASE WHEN f.sku_ext_code<>'' THEN 'direct'
             WHEN f.sku_id IS NOT NULL AND m.candidates=1 THEN 'sku_id'
             WHEN f.sku_id IS NULL AND p.candidates=1 AND NOT p.incomplete THEN 'po_single' ELSE 'unattributed' END attribution_method,
        CASE WHEN f.sku_id IS NOT NULL AND COALESCE(m.candidates,0)<>1 THEN 'SKU ID 未找到唯一编码'
             WHEN f.po_no='' THEN 'PO 单号缺失' WHEN COALESCE(p.incomplete,false) THEN 'PO 商品编码不完整'
             WHEN p.candidates>1 THEN '多 SKU PO 待分摊' ELSE '未找到 PO 商品' END unresolved_reason,
        COALESCE(NULLIF(f.sku_name,''),CASE WHEN p.candidates=1 THEN p.sku_name END,'') resolved_sku_name
 FROM temu_profit_settled_flows f LEFT JOIN sku_map m USING(shop_key,sku_id)
 LEFT JOIN orders p USING(shop_key,po_no)
)
SELECT id,shop_key,flow_id,po_no,document_no,trade_type,settle_amount,currency,received_at,
       sku_id,sku_ext_code AS source_sku,platform_sku,resolved_sku_name AS sku_name,attribution_method,candidate_skus,
       CASE WHEN attribution_method='unattributed' THEN unresolved_reason ELSE '' END reason
FROM matched;
