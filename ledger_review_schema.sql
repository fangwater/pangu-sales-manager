CREATE TABLE IF NOT EXISTS profit_ledger_reviews (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 entry_key text NOT NULL UNIQUE,shop_key text NOT NULL,source_fingerprint text NOT NULL,
 source_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
 kind text NOT NULL CHECK(kind IN ('allocation','non_order')),
 method text NOT NULL CHECK(method IN ('declared_value','manual')),
 allocations jsonb NOT NULL CHECK(jsonb_typeof(allocations)='array'),
 reference text NOT NULL CHECK(length(btrim(reference))>0),
 version integer NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),deleted_at timestamptz
);
ALTER TABLE profit_ledger_reviews ADD COLUMN IF NOT EXISTS source_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE TABLE IF NOT EXISTS profit_ledger_review_history (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 review_id bigint NOT NULL REFERENCES profit_ledger_reviews(id),
 action text NOT NULL CHECK(action IN ('save','withdraw')),
 before_payload jsonb,after_payload jsonb,created_at timestamptz NOT NULL DEFAULT now()
);
CREATE OR REPLACE FUNCTION profit_ledger_fingerprint(r jsonb) RETURNS text LANGUAGE sql STABLE AS $fingerprint$
 SELECT md5(jsonb_build_array(r->'entry_key',r->'shop_key',r->'source_reference',r->'fee_name',r->'original_amount',r->'amount',r->'currency',
 r->'order_reference',r->'source_sku',r->'sku_id',r->'package_no',r->'tracking_no',
 extract(epoch FROM (r->>'occurred_at')::timestamptz),extract(epoch FROM (r->>'paid_at')::timestamptz),
 r->'candidate_pos',r->'package_pos',r->'tracking_pos',r->'order_status',r->'sku_status',r->'platform_sku',r->'products')::text)
$fingerprint$;
