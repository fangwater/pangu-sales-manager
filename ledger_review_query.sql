WITH ledger AS MATERIALIZED({{LEDGER_ROWS}}),basis AS MATERIALIZED(SELECT * FROM profit_allocation_basis),
reviews AS MATERIALIZED(SELECT r.* FROM profit_ledger_reviews r JOIN ledger l USING(entry_key) WHERE r.deleted_at IS NULL),
 review_products AS MATERIALIZED(SELECT p.* FROM profit_ledger_products p WHERE EXISTS(SELECT 1 FROM reviews r CROSS JOIN LATERAL jsonb_array_elements(r.allocations) a WHERE p.shop_key=r.shop_key AND p.po_no=a->>'po_no')),
 review_targets AS MATERIALIZED(
 SELECT r.entry_key,BOOL_AND(COALESCE(EXISTS(SELECT 1 FROM jsonb_array_elements(p.products) item WHERE item->>'platform_sku'=a->>'platform_sku'),false)
 AND COALESCE((r.method='manual' OR a->'basis_evidence'=jsonb_build_object('currency',b.currency,'items',b.items,'basis_source',b.basis_source)),false)) valid,
 SUM((a->>'amount')::numeric) amount,COUNT(*) parts
 FROM reviews r CROSS JOIN LATERAL jsonb_array_elements(r.allocations) a
 LEFT JOIN review_products p ON p.shop_key=r.shop_key AND p.po_no=a->>'po_no'
 LEFT JOIN basis b ON b.shop_key=r.shop_key AND b.po_no=a->>'po_no'
 GROUP BY r.entry_key),
 review_checks AS MATERIALIZED(SELECT r.*,r.source_fingerprint=profit_ledger_fingerprint(to_jsonb(l)) AND
 CASE WHEN r.kind='non_order' THEN cardinality(l.candidate_pos)=0 AND l.order_reference=''
 ELSE COALESCE(t.valid,false) AND t.parts>0 AND t.amount=l.amount END valid
 FROM reviews r JOIN ledger l USING(entry_key) LEFT JOIN review_targets t USING(entry_key))
 SELECT l.*,0::numeric non_order_amount,NULL::jsonb review FROM ledger l
 WHERE NOT EXISTS(SELECT 1 FROM review_checks r WHERE r.entry_key=l.entry_key)
 UNION ALL
 SELECT revised.*,
 CASE WHEN r.valid AND r.kind='non_order' THEN l.amount ELSE 0::numeric END non_order_amount,
 jsonb_build_object('id',r.id,'version',r.version,'kind',r.kind,'method',r.method,'reference',r.reference,'orders',jsonb_path_query_array(r.allocations,'$[*].po_no'),'updated_at',r.updated_at,'status',CASE WHEN r.valid THEN 'active' ELSE 'stale' END) review
 FROM ledger l JOIN review_checks r USING(entry_key)
 CROSS JOIN LATERAL jsonb_populate_record(NULL::profit_ledger_allocated,to_jsonb(l)||
 jsonb_build_object('allocations',CASE WHEN r.valid AND r.kind='allocation' THEN r.allocations ELSE '[]'::jsonb END,
 'allocation_status',CASE WHEN NOT r.valid THEN 'unattributed' WHEN r.kind='non_order' THEN 'non_order' ELSE 'allocated' END,
 'attributed_amount',CASE WHEN r.valid AND r.kind='allocation' THEN l.amount ELSE 0::numeric END,
 'unattributed_amount',CASE WHEN r.valid THEN 0::numeric ELSE l.amount END,
 'allocation_reason',CASE WHEN NOT r.valid THEN '人工核对依据已变化，须重新核对或撤销' ELSE '' END)) revised
