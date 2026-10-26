WITH ledger AS MATERIALIZED({{LEDGER_ROWS}}),basis AS MATERIALIZED(SELECT * FROM profit_allocation_basis),
 eligible AS MATERIALIZED (
 SELECT l.entry_key,l.shop_key,l.amount,l.candidate_pos,policy.method,policy.version,
 CASE WHEN ABS(l.amount)=ROUND(ABS(l.amount),2) THEN 0.01::numeric ELSE 0.0001::numeric END quantum
 FROM ledger l CROSS JOIN profit_allocation_policy policy
 WHERE l.sku_status='unattributed' AND l.order_status<>'conflict' AND cardinality(l.candidate_pos)>0
 AND l.sku_id IS NULL AND policy.method<>'manual'
 ), candidate_basis AS MATERIALIZED (
 SELECT e.*,po.po_no,b.basis_source,b.currency basis_currency,b.items
 FROM eligible e CROSS JOIN LATERAL unnest(e.candidate_pos) po(po_no)
 LEFT JOIN basis b ON b.shop_key=e.shop_key AND b.po_no=po.po_no
 ), complete AS (
 SELECT entry_key FROM candidate_basis GROUP BY entry_key
 HAVING BOOL_AND(items IS NOT NULL) AND COUNT(DISTINCT basis_currency)=1
 ), weights AS (
 SELECT e.*,i->>'platform_sku' platform_sku,i->>'sku_name' sku_name,
 (i->>'quantity')::numeric quantity,(i->>'declared_price')::numeric declared_price,(i->>'declared_value')::numeric declared_value,
 CASE e.method WHEN 'quantity' THEN (i->>'quantity')::numeric ELSE (i->>'declared_value')::numeric END weight
 FROM candidate_basis e JOIN complete USING(entry_key) CROSS JOIN LATERAL jsonb_array_elements(e.items) i
 ), ratios AS (
 SELECT *,SUM(weight) OVER(PARTITION BY entry_key) total_weight FROM weights
 ), floors AS (
 SELECT *,FLOOR(ABS(amount)/quantum*weight/total_weight) base_units,
 ABS(amount)/quantum*weight/total_weight-FLOOR(ABS(amount)/quantum*weight/total_weight) remainder FROM ratios
 ), rounded AS (
 SELECT *,ROW_NUMBER() OVER(PARTITION BY entry_key ORDER BY remainder DESC,po_no,platform_sku) remainder_rank,
 ABS(amount)/quantum-SUM(base_units) OVER(PARTITION BY entry_key) remaining_units FROM floors
 ), splits AS (
 SELECT *,SIGN(amount)*quantum*(base_units+CASE WHEN remainder_rank<=remaining_units THEN 1 ELSE 0 END) allocated_amount FROM rounded
 ), allocated AS (
 SELECT entry_key,jsonb_agg(jsonb_build_object('po_no',po_no,'platform_sku',platform_sku,'sku_name',sku_name,
 'basis_source',basis_source,'basis_currency',basis_currency,'quantity',quantity,'declared_price',declared_price,'declared_value',declared_value,
 'weight',weight,'total_weight',total_weight,'ratio',weight/total_weight,'amount',allocated_amount,
 'rounding_adjustment',SIGN(amount)*quantum*CASE WHEN remainder_rank<=remaining_units THEN 1 ELSE 0 END,
 'method',method,'policy_version',version) ORDER BY po_no,platform_sku) allocations,SUM(allocated_amount) allocated_amount
 FROM splits GROUP BY entry_key
 )
 SELECT l.*,COALESCE(a.allocations,'[]'::jsonb) allocations,
 CASE WHEN a.entry_key IS NOT NULL THEN 'allocated' ELSE l.sku_status END allocation_status,
 CASE WHEN l.sku_status='matched' THEN l.amount ELSE COALESCE(a.allocated_amount,0) END attributed_amount,
 CASE WHEN l.sku_status='matched' OR a.entry_key IS NOT NULL THEN 0::numeric ELSE l.amount END unattributed_amount,
 CASE WHEN a.entry_key IS NOT NULL OR l.sku_status='matched' THEN ''
 WHEN (SELECT method FROM profit_allocation_policy WHERE id=1)='manual' THEN l.reason||'；已暂停自动分摊'
 WHEN l.sku_id IS NULL AND cardinality(l.candidate_pos)>0 AND l.order_status<>'conflict' THEN l.reason||'；商品申报金额或数量不完整' ELSE l.reason END allocation_reason
 FROM ledger l LEFT JOIN allocated a USING(entry_key)
