-- TEMU 利润计算源表：1:1 映射 TEMU利润计算/*.xlsx 全部列。
-- 相同列结构的工作簿合并为一张表，用 phase / destination 区分来源文件。
--
-- 发货面单费-已出账 / 发货面单费-待出账
--   -> temu_profit_shipping_label_fees
-- 退货面单费-退至商家仓 / 退货面单费-退至第三方仓
--   -> temu_profit_return_label_fees
-- 对账中心-账务明细 / 支出-买家拒付
--   -> temu_profit_buyer_chargebacks
-- 对账中心-账务明细 / 支出-履约违规-1、支出-履约违规-2
--   -> temu_profit_fulfillment_violations
-- 对账中心-账务明细 / 其他-退货面单费平台承担
--   -> temu_profit_platform_return_label_fees
-- 对账中心-账务明细 / 支出-处置费
--   -> temu_profit_disposal_fees
-- 对账中心-账务明细 / 结算  与  结算数据-已到账款项-PO明细账单
--   -> temu_profit_settled_flows
--   （两份列结构相同；样例中 PO 明细是账务明细结算的子集）
-- 结算数据-已到账款项-po聚合账单
--   -> temu_profit_settled_parent_orders + temu_profit_settled_parent_skus
-- 结算数据-待处理款项
--   -> temu_profit_unsettle_orders + temu_profit_unsettle_skus
--
-- 聚合账单与待处理款项的「商品信息 * 销售件数」在 xlsx 里是合并单元格：
-- PO 金额在主表一行，续行 SKU 落到子表，line_no 从 1 开始按文件顺序编号。

CREATE TABLE IF NOT EXISTS temu_profit_shipping_label_fees (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    shop_key text NOT NULL DEFAULT '',
    billing_phase text NOT NULL CHECK (billing_phase IN ('posted', 'pending')), -- posted=已出账 pending=待出账
    package_no text NOT NULL,                 -- 包裹号
    tracking_no text NOT NULL,                -- 运单号
    carrier_code text NOT NULL DEFAULT '',    -- 服务商code
    bill_type text NOT NULL DEFAULT '',       -- 账单类型
    remark text NOT NULL DEFAULT '',          -- 备注
    freight_amount numeric(16,4) NOT NULL,    -- 运费(单位元)-支出为负、退款为正
    currency text NOT NULL DEFAULT '',        -- 币种
    reconcile_status text NOT NULL DEFAULT '', -- 对账单状态
    occurred_at timestamptz,                  -- 支出/退款时间(时区：GMT+8)；待出账为 -- 时为空
    imported_at timestamptz NOT NULL DEFAULT now()
);

-- 业务键不含金额/状态/时间：同一包裹再次上传时覆盖可变字段。
DROP INDEX IF EXISTS temu_profit_shipping_label_fees_natural_idx;
CREATE UNIQUE INDEX IF NOT EXISTS temu_profit_shipping_label_fees_upsert_idx
    ON temu_profit_shipping_label_fees (shop_key, billing_phase, package_no, tracking_no, bill_type, remark);
CREATE INDEX IF NOT EXISTS temu_profit_shipping_label_fees_package_idx
    ON temu_profit_shipping_label_fees (package_no);
CREATE INDEX IF NOT EXISTS temu_profit_shipping_label_fees_tracking_idx
    ON temu_profit_shipping_label_fees (tracking_no);
CREATE INDEX IF NOT EXISTS temu_profit_shipping_label_fees_occurred_idx
    ON temu_profit_shipping_label_fees (occurred_at DESC);

CREATE TABLE IF NOT EXISTS temu_profit_return_label_fees (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    shop_key text NOT NULL DEFAULT '',
    return_destination text NOT NULL CHECK (return_destination IN ('merchant_warehouse', 'third_party_warehouse')), -- 商家仓 / 第三方仓
    fund_bill_id text NOT NULL,               -- 账务单ID
    tracking_no text NOT NULL DEFAULT '',     -- 运单号
    po_no text NOT NULL DEFAULT '',           -- PO单号
    bill_type text NOT NULL DEFAULT '',       -- 账单类型
    currency text NOT NULL DEFAULT '',        -- 币种
    bill_remark text NOT NULL DEFAULT '',     -- 账单备注
    amount numeric(16,4) NOT NULL,            -- 金额
    occurred_at timestamptz,                  -- 账单支出/退款时间
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shop_key, fund_bill_id)
);

CREATE INDEX IF NOT EXISTS temu_profit_return_label_fees_po_idx
    ON temu_profit_return_label_fees (po_no);
CREATE INDEX IF NOT EXISTS temu_profit_return_label_fees_tracking_idx
    ON temu_profit_return_label_fees (tracking_no);

CREATE TABLE IF NOT EXISTS temu_profit_buyer_chargebacks (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    shop_key text NOT NULL DEFAULT '',
    violation_order_no text NOT NULL,         -- 违规单号
    amount numeric(16,4) NOT NULL,            -- 支出金额
    currency text NOT NULL DEFAULT '',        -- 币种
    accounted_at timestamptz,                 -- 账务时间
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shop_key, violation_order_no)
);

CREATE INDEX IF NOT EXISTS temu_profit_buyer_chargebacks_order_idx
    ON temu_profit_buyer_chargebacks (violation_order_no);

CREATE TABLE IF NOT EXISTS temu_profit_fulfillment_violations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    shop_key text NOT NULL DEFAULT '',
    source_sheet text NOT NULL CHECK (source_sheet IN ('fulfillment_delay', 'fulfillment_false_ship')), -- 履约违规-1 / 履约违规-2
    violation_no text NOT NULL,               -- 违规编号
    order_no text NOT NULL DEFAULT '',        -- 订单编号
    violation_type text NOT NULL DEFAULT '',  -- 违规类型
    amount numeric(16,4) NOT NULL,            -- 支出金额
    currency text NOT NULL DEFAULT '',        -- 币种
    accounted_at timestamptz,                 -- 账务时间
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shop_key, violation_no)
);

CREATE INDEX IF NOT EXISTS temu_profit_fulfillment_violations_order_idx
    ON temu_profit_fulfillment_violations (order_no);
CREATE INDEX IF NOT EXISTS temu_profit_fulfillment_violations_type_idx
    ON temu_profit_fulfillment_violations (violation_type);

CREATE TABLE IF NOT EXISTS temu_profit_platform_return_label_fees (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    shop_key text NOT NULL DEFAULT '',
    order_no text NOT NULL,                   -- 订单编号
    amount numeric(16,4) NOT NULL,            -- 收支金额
    currency text NOT NULL DEFAULT '',        -- 币种
    remark text NOT NULL DEFAULT '',          -- 备注
    accounted_at timestamptz,                 -- 账务时间
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shop_key, order_no, remark)
);

CREATE INDEX IF NOT EXISTS temu_profit_platform_return_label_fees_order_idx
    ON temu_profit_platform_return_label_fees (order_no);

CREATE TABLE IF NOT EXISTS temu_profit_disposal_fees (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    shop_key text NOT NULL DEFAULT '',
    sku_id bigint NOT NULL,                   -- SKU ID
    tracking_type text NOT NULL DEFAULT '',   -- 运单号类型
    tracking_no text NOT NULL,                -- 运单号
    destroy_qty integer NOT NULL DEFAULT 0,   -- 销毁件数
    returned_at timestamptz,                  -- 回仓时间
    destroy_fee numeric(16,4) NOT NULL,       -- 销毁费用
    currency text NOT NULL DEFAULT '',        -- 币种
    accounted_at timestamptz,                 -- 账务时间
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shop_key, sku_id, tracking_no)
);

CREATE INDEX IF NOT EXISTS temu_profit_disposal_fees_tracking_idx
    ON temu_profit_disposal_fees (tracking_no);
CREATE INDEX IF NOT EXISTS temu_profit_disposal_fees_accounted_idx
    ON temu_profit_disposal_fees (accounted_at DESC);

CREATE TABLE IF NOT EXISTS temu_profit_settled_flows (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    shop_key text NOT NULL DEFAULT '',
    flow_id text NOT NULL,                    -- 结算流水ID
    batch_no text NOT NULL DEFAULT '',        -- 批次号
    po_no text NOT NULL DEFAULT '',           -- PO单号
    document_no text NOT NULL DEFAULT '',     -- 单据号
    trade_type text NOT NULL DEFAULT '',      -- 交易类型
    settle_amount numeric(16,4) NOT NULL,     -- 结算金额
    sku_id bigint,                            -- SKU ID（运费/冲回行可空）
    sku_name text NOT NULL DEFAULT '',        -- SKU名称
    sku_ext_code text NOT NULL DEFAULT '',    -- SKU货号
    quantity numeric(16,4),                   -- 件数
    declared_price numeric(16,4),             -- 申报价格
    is_activity_price boolean,                -- 是否活动价
    currency text NOT NULL DEFAULT '',        -- 币种
    total_declared_price numeric(16,4),       -- 总申报价格
    sku_coupon_amount numeric(16,4) NOT NULL DEFAULT 0, -- 单品优惠券金额
    shop_coupon_amount numeric(16,4) NOT NULL DEFAULT 0, -- 店铺满减券优惠金额
    declared_discount_amount numeric(16,4) NOT NULL DEFAULT 0, -- 申报价格折扣金额
    activity_freight_discount_amount numeric(16,4) NOT NULL DEFAULT 0, -- 活动运费主动减免金额
    received_at timestamptz,                  -- 到账时间
    remark text NOT NULL DEFAULT '',          -- 备注
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shop_key, flow_id)
);

CREATE INDEX IF NOT EXISTS temu_profit_settled_flows_po_idx
    ON temu_profit_settled_flows (po_no);
CREATE INDEX IF NOT EXISTS temu_profit_settled_flows_sku_idx
    ON temu_profit_settled_flows (sku_id) WHERE sku_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS temu_profit_settled_flows_trade_idx
    ON temu_profit_settled_flows (trade_type, received_at DESC);
CREATE INDEX IF NOT EXISTS temu_profit_settled_flows_batch_idx
    ON temu_profit_settled_flows (batch_no);
CREATE INDEX IF NOT EXISTS temu_profit_settled_flows_received_idx
    ON temu_profit_settled_flows (received_at DESC);

CREATE TABLE IF NOT EXISTS temu_profit_settled_parent_orders (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    shop_key text NOT NULL DEFAULT '',
    po_no text NOT NULL,                      -- PO单号
    currency text NOT NULL DEFAULT '',        -- 币种
    sales_receipt numeric(16,4) NOT NULL DEFAULT 0, -- 销售回款
    sales_receipt_after_discount numeric(16,4) NOT NULL DEFAULT 0, -- 销售回款已减优惠
    sales_chargeback numeric(16,4) NOT NULL DEFAULT 0, -- 销售冲回
    freight_receipt numeric(16,4) NOT NULL DEFAULT 0, -- 运费回款
    freight_receipt_after_discount numeric(16,4) NOT NULL DEFAULT 0, -- 运费回款已减优惠
    freight_chargeback numeric(16,4) NOT NULL DEFAULT 0, -- 运费冲回
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shop_key, po_no)
);

CREATE TABLE IF NOT EXISTS temu_profit_settled_parent_skus (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    parent_order_id bigint NOT NULL REFERENCES temu_profit_settled_parent_orders(id) ON DELETE CASCADE,
    line_no integer NOT NULL CHECK (line_no > 0), -- 文件内该 PO 的 SKU 行序，从 1 起
    sku_id bigint NOT NULL,                   -- SKU ID
    sku_name text NOT NULL DEFAULT '',        -- SKU名称
    sku_ext_code text NOT NULL DEFAULT '',    -- SKU货号
    quantity numeric(16,4) NOT NULL,          -- 件数
    declared_price numeric(16,4) NOT NULL,    -- 申报价格
    is_activity_price boolean,                -- 是否活动价
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (parent_order_id, line_no)
);

CREATE INDEX IF NOT EXISTS temu_profit_settled_parent_skus_sku_idx
    ON temu_profit_settled_parent_skus (sku_id);

CREATE TABLE IF NOT EXISTS temu_profit_unsettle_orders (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    shop_key text NOT NULL DEFAULT '',
    po_no text NOT NULL,                      -- PO单号
    currency text NOT NULL DEFAULT '',        -- 币种
    sales_receipt numeric(16,4) NOT NULL DEFAULT 0, -- 销售回款
    sales_receipt_after_discount numeric(16,4) NOT NULL DEFAULT 0, -- 销售回款已减优惠
    sales_chargeback numeric(16,4) NOT NULL DEFAULT 0, -- 销售冲回
    freight_receipt numeric(16,4) NOT NULL DEFAULT 0, -- 运费回款
    freight_chargeback numeric(16,4) NOT NULL DEFAULT 0, -- 运费冲回
    freight_receipt_after_discount numeric(16,4) NOT NULL DEFAULT 0, -- 运费回款已减优惠
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shop_key, po_no)
);

CREATE TABLE IF NOT EXISTS temu_profit_unsettle_skus (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    unsettle_order_id bigint NOT NULL REFERENCES temu_profit_unsettle_orders(id) ON DELETE CASCADE,
    line_no integer NOT NULL CHECK (line_no > 0), -- 文件内该 PO 的 SKU 行序，从 1 起
    sku_id bigint NOT NULL,                   -- SKU ID
    sku_name text NOT NULL DEFAULT '',        -- SKU名称
    quantity numeric(16,4) NOT NULL,          -- 件数
    declared_price numeric(16,4) NOT NULL,    -- 申报价格
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (unsettle_order_id, line_no)
);

CREATE INDEX IF NOT EXISTS temu_profit_unsettle_skus_sku_idx
    ON temu_profit_unsettle_skus (sku_id);

CREATE TABLE IF NOT EXISTS temu_profit_import_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    shop_key text NOT NULL DEFAULT '',
    source_kind text NOT NULL CHECK (source_kind IN ('zip', 'xlsx')),
    source_name text NOT NULL,
    status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    files_imported integer NOT NULL DEFAULT 0,
    rows_upserted integer NOT NULL DEFAULT 0,
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_message text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE INDEX IF NOT EXISTS temu_profit_import_runs_started_idx
    ON temu_profit_import_runs (started_at DESC);

-- 已存在库：去掉把金额/时间算进唯一键的旧约束，改为可覆盖。
DO $$
DECLARE rec record;
BEGIN
  FOR rec IN
    SELECT r.relname AS table_name, c.conname
    FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    WHERE c.contype = 'u'
      AND r.relname IN ('temu_profit_buyer_chargebacks', 'temu_profit_platform_return_label_fees')
      AND c.conname NOT IN (
        'temu_profit_buyer_chargebacks_shop_key_violation_order_no_key',
        'temu_profit_platform_return_label_fees_shop_key_order_no_remark_key'
      )
  LOOP
    EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', rec.table_name, rec.conname);
  END LOOP;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS temu_profit_buyer_chargebacks_upsert_idx
    ON temu_profit_buyer_chargebacks (shop_key, violation_order_no);
CREATE UNIQUE INDEX IF NOT EXISTS temu_profit_platform_return_label_fees_upsert_idx
    ON temu_profit_platform_return_label_fees (shop_key, order_no, remark);
