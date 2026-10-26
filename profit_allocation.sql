CREATE TABLE IF NOT EXISTS profit_allocation_policy (
 id integer PRIMARY KEY CHECK(id=1),method text NOT NULL CHECK(method IN ('declared_value','quantity','manual')),
 cost_method text NOT NULL DEFAULT 'fifo' CHECK(cost_method='fifo'),version integer NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO profit_allocation_policy(id,method) VALUES(1,'declared_value') ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS profit_allocation_policy_history (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,version integer NOT NULL UNIQUE,method text NOT NULL,cost_method text NOT NULL,created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO profit_allocation_policy_history(version,method,cost_method) SELECT version,method,cost_method FROM profit_allocation_policy ON CONFLICT DO NOTHING;

-- A complete, single-currency declaration is required for every candidate PO.
-- Repeated sales receipts cannot establish an original quantity reliably.
CREATE OR REPLACE VIEW profit_allocation_basis AS
 WITH identities AS MATERIALIZED(SELECT * FROM profit_ledger_identity),
 products AS MATERIALIZED(SELECT * FROM profit_ledger_products),
 evidence AS (
 SELECT p.shop_key,p.po_no,1 priority,'parent_sku'::text basis_source,p.currency,
 COALESCE(NULLIF(k.sku_ext_code,''),CASE WHEN m.candidates=1 THEN m.platform_sku END,'') platform_sku,
 k.quantity,k.declared_price,k.sku_name
 FROM temu_profit_settled_parent_orders p JOIN temu_profit_settled_parent_skus k ON k.parent_order_id=p.id
 LEFT JOIN identities m ON m.shop_key=p.shop_key AND m.sku_id=k.sku_id
 UNION ALL
 SELECT f.shop_key,f.po_no,2,'sales_receipt',f.currency,COALESCE(NULLIF(f.sku_ext_code,''),CASE WHEN m.candidates=1 THEN m.platform_sku END,''),f.quantity,f.declared_price,f.sku_name
 FROM temu_profit_settled_flows f LEFT JOIN identities m USING(shop_key,sku_id) WHERE trade_type='销售回款'
 ), grouped AS (
 SELECT shop_key,po_no,priority,basis_source,MIN(currency) currency,COUNT(DISTINCT currency) currencies,
 BOOL_AND(currency<>'' AND platform_sku<>'' AND quantity>0 AND declared_price>0 AND quantity IS NOT NULL AND declared_price IS NOT NULL) valid,
 COUNT(*) rows,COUNT(DISTINCT platform_sku) skus,
 jsonb_agg(jsonb_build_object('platform_sku',platform_sku,'quantity',quantity,'declared_price',declared_price,'declared_value',quantity*declared_price,'sku_name',sku_name) ORDER BY platform_sku) items
 FROM evidence GROUP BY 1,2,3,4
 ), complete AS (
 SELECT g.*,ROW_NUMBER() OVER(PARTITION BY g.shop_key,g.po_no ORDER BY priority) choice
 FROM grouped g JOIN products p USING(shop_key,po_no)
 WHERE valid AND currencies=1 AND skus=p.candidate_skus AND NOT p.incomplete AND rows=skus
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(p.products) x WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements(g.items) y WHERE y->>'platform_sku'=x->>'platform_sku'))
 )
 SELECT shop_key,po_no,basis_source,currency,items FROM complete WHERE choice=1;

