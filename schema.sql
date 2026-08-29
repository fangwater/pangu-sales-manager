CREATE TABLE IF NOT EXISTS warehouses (
    code text PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    active boolean NOT NULL DEFAULT true,
    source text NOT NULL DEFAULT 'xlwms',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS canonical_skus (
    warehouse_sku text PRIMARY KEY,
    product_name text NOT NULL DEFAULT '',
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sku_mappings (
    platform text NOT NULL,
    shop_key text NOT NULL,
    platform_sku text NOT NULL,
    warehouse_sku text NOT NULL REFERENCES canonical_skus(warehouse_sku),
    conversion_factor numeric(14,4) NOT NULL DEFAULT 1 CHECK (conversion_factor > 0),
    mapping_source text NOT NULL,
    mapping_status text NOT NULL CHECK (mapping_status IN ('mapped', 'identity', 'inferred', 'manual', 'unmapped')),
    product_name text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (platform, shop_key, platform_sku)
);

CREATE TABLE IF NOT EXISTS normalized_orders (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    platform text NOT NULL CHECK (platform IN ('temu', 'shein')),
    shop_key text NOT NULL,
    source_order_no text NOT NULL,
    source_status text NOT NULL DEFAULT '',
    normalized_status text NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL,
    occurred_at_source text NOT NULL,
    warehouse_code text NOT NULL DEFAULT '',
    sales_eligible boolean NOT NULL DEFAULT true,
    source_first_seen_at timestamptz,
    source_updated_at timestamptz,
    raw_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    synced_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (platform, shop_key, source_order_no)
);

CREATE TABLE IF NOT EXISTS normalized_order_lines (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id bigint NOT NULL REFERENCES normalized_orders(id) ON DELETE CASCADE,
    source_line_key text NOT NULL,
    platform_sku text NOT NULL,
    warehouse_sku text NOT NULL REFERENCES canonical_skus(warehouse_sku),
    product_name text NOT NULL DEFAULT '',
    variant_name text NOT NULL DEFAULT '',
    warehouse_code text NOT NULL DEFAULT '',
    quantity numeric(14,4) NOT NULL CHECK (quantity >= 0),
    conversion_factor numeric(14,4) NOT NULL DEFAULT 1 CHECK (conversion_factor > 0),
    warehouse_quantity numeric(14,4) NOT NULL CHECK (warehouse_quantity >= 0),
    unit_price numeric(14,4),
    currency text NOT NULL DEFAULT '',
    raw_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    synced_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, source_line_key)
);

CREATE TABLE IF NOT EXISTS warehouse_inventory (
    warehouse_code text NOT NULL,
    warehouse_sku text NOT NULL REFERENCES canonical_skus(warehouse_sku),
    warehouse_name text NOT NULL DEFAULT '',
    total_quantity numeric(16,4) NOT NULL DEFAULT 0,
    available_quantity numeric(16,4) NOT NULL DEFAULT 0,
    locked_quantity numeric(16,4) NOT NULL DEFAULT 0,
    in_transit_quantity numeric(16,4) NOT NULL DEFAULT 0,
    statistic_date date,
    source_updated_at timestamptz,
    synced_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (warehouse_code, warehouse_sku)
);

CREATE TABLE IF NOT EXISTS sync_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    orders_synced integer NOT NULL DEFAULT 0,
    lines_synced integer NOT NULL DEFAULT 0,
    inventory_synced integer NOT NULL DEFAULT 0,
    error_message text NOT NULL DEFAULT ''
);

ALTER TABLE sync_runs
    ADD COLUMN IF NOT EXISTS sync_mode text NOT NULL DEFAULT 'incremental';

UPDATE sync_runs
SET sync_mode='full'
WHERE id=(SELECT MAX(id) FROM sync_runs WHERE status='succeeded')
  AND NOT EXISTS (SELECT 1 FROM sync_runs WHERE sync_mode='full');

CREATE INDEX IF NOT EXISTS normalized_orders_occurred_idx
    ON normalized_orders (occurred_at DESC);
CREATE INDEX IF NOT EXISTS normalized_orders_filter_idx
    ON normalized_orders (platform, shop_key, occurred_at DESC)
    WHERE sales_eligible;
CREATE INDEX IF NOT EXISTS normalized_order_lines_sku_idx
    ON normalized_order_lines (warehouse_sku, order_id);
CREATE INDEX IF NOT EXISTS normalized_order_lines_warehouse_idx
    ON normalized_order_lines (warehouse_code, order_id);
CREATE INDEX IF NOT EXISTS sku_mappings_status_idx
    ON sku_mappings (mapping_status, platform, shop_key);
CREATE INDEX IF NOT EXISTS sync_runs_full_idx
    ON sync_runs (completed_at DESC) WHERE status='succeeded' AND sync_mode='full';

CREATE TABLE IF NOT EXISTS temu_activity_snapshot_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    captured_at timestamptz NOT NULL UNIQUE,
    started_at timestamptz NOT NULL,
    enrollment_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS temu_activity_enrollment_snapshots (
    snapshot_id bigint NOT NULL REFERENCES temu_activity_snapshot_runs(id) ON DELETE CASCADE,
    enroll_id bigint NOT NULL,
    skc_id bigint NOT NULL,
    activity_type bigint NOT NULL DEFAULT 0,
    activity_type_name text NOT NULL DEFAULT '',
    activity_thematic_id bigint NOT NULL DEFAULT 0,
    activity_thematic_name text NOT NULL DEFAULT '',
    activity_stock bigint NOT NULL DEFAULT 0,
    remaining_activity_stock bigint NOT NULL DEFAULT 0,
    previous_remaining_activity_stock bigint,
    interval_consumed_stock bigint NOT NULL DEFAULT 0,
    interval_increased_stock bigint NOT NULL DEFAULT 0,
    cumulative_consumed_stock bigint NOT NULL DEFAULT 0,
    enrollment_sku_count integer NOT NULL DEFAULT 0,
    PRIMARY KEY (snapshot_id, enroll_id, skc_id)
);

CREATE TABLE IF NOT EXISTS temu_skc_activity_state_snapshots (
    snapshot_id bigint NOT NULL REFERENCES temu_activity_snapshot_runs(id) ON DELETE CASCADE,
    skc_id bigint NOT NULL,
    status text NOT NULL CHECK (status IN ('confirmed', 'warning')),
    active_enroll_id bigint,
    previous_active_enroll_id bigint,
    candidate_enroll_ids bigint[] NOT NULL DEFAULT '{}',
    evidence_enroll_ids bigint[] NOT NULL DEFAULT '{}',
    state_started_at timestamptz NOT NULL,
    last_evidence_at timestamptz,
    carried_forward boolean NOT NULL DEFAULT false,
    reason text NOT NULL DEFAULT '',
    PRIMARY KEY (snapshot_id, skc_id)
);

CREATE TABLE IF NOT EXISTS temu_sku_price_intervals (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sku_id bigint NOT NULL,
    skc_id bigint NOT NULL,
    status text NOT NULL CHECK (status IN ('confirmed', 'warning')),
    active_enroll_id bigint,
    candidate_enroll_ids bigint[] NOT NULL DEFAULT '{}',
    currency text NOT NULL DEFAULT '',
    daily_price bigint NOT NULL DEFAULT 0,
    activity_price bigint NOT NULL DEFAULT 0,
    price bigint NOT NULL DEFAULT 0,
    price_source text NOT NULL CHECK (price_source IN ('activity', 'daily', 'unresolved')),
    reason text NOT NULL DEFAULT '',
    start_at timestamptz NOT NULL,
    update_at timestamptz NOT NULL,
    end_at timestamptz,
    first_snapshot_id bigint NOT NULL REFERENCES temu_activity_snapshot_runs(id),
    last_snapshot_id bigint NOT NULL REFERENCES temu_activity_snapshot_runs(id)
);

CREATE INDEX IF NOT EXISTS temu_activity_enrollment_history_idx
    ON temu_activity_enrollment_snapshots (enroll_id, skc_id, snapshot_id DESC);
CREATE INDEX IF NOT EXISTS temu_skc_activity_state_history_idx
    ON temu_skc_activity_state_snapshots (skc_id, snapshot_id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS temu_sku_price_intervals_open_idx
    ON temu_sku_price_intervals (sku_id) WHERE end_at IS NULL;
CREATE INDEX IF NOT EXISTS temu_sku_price_intervals_history_idx
    ON temu_sku_price_intervals (sku_id, start_at DESC);

ALTER TABLE temu_skc_activity_state_snapshots
    DROP CONSTRAINT IF EXISTS temu_skc_activity_state_snapshots_status_check;
UPDATE temu_skc_activity_state_snapshots
SET status='warning'
WHERE status <> 'confirmed';
ALTER TABLE temu_skc_activity_state_snapshots
    ADD CONSTRAINT temu_skc_activity_state_snapshots_status_check
    CHECK (status IN ('confirmed', 'warning'));

DROP TABLE IF EXISTS temu_activity_sku_price_snapshots;
DROP TABLE IF EXISTS temu_sku_price_state_snapshots;
