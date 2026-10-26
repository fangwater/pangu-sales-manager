# Pangu Sales Manager

Pangu 销售管理系统将 TEMU、SHEIN 订单与 XLWMS 仓库库存同步到本机 PostgreSQL，统一平台差异后提供销售分析、SKU 预测、仓库视图和映射治理。

## 数据口径

- `normalized_orders`：统一平台、店铺、订单状态、销售时间、仓库归属和原始 JSON。
- `normalized_order_lines`：同时保留平台件数、仓库 SKU、换算系数和仓库换算量。
- `sku_mappings`：平台 SKU 到仓库 SKU 的映射。TEMU 默认使用 `ext_code` 同码映射；SHEIN 保留已有权威/人工映射，新 SKU 使用 `sellerSku` 推断，缺失时标记为待确认。
- `warehouse_inventory`：XLWMS `integrated` 库存快照，按仓库与仓库 SKU 存储。
- SHEIN 使用平台下单时间；TEMU 当前源表没有下单时间，使用 `first_seen_at` 并在 `occurred_at_source` 明确记录。
- 退款/取消订单不进入销量统计，原始订单仍保留在标准订单库。
- SHEIN 同时按商品状态剔除部分退款：优先使用 `newGoodsStatus=6`，新状态缺失时使用旧字段 `goodsStatus=3`。同一订单/SKU 的 `quantity` 是未退款商品件数，仓库数量按此换算；全部退款的 SKU 保留零数量行。原始商品列表、原始件数和退款件数保留在订单行 `raw_payload.items/original_quantity/refunded_quantity`。每次同步覆盖当前结果，不累积扣减。历史修复通过存量订单完整同步重算。

## 预测

预测使用最近 7 日与 28 日加权移动平均叠加 28 日线性趋势，按最近 90 天补零序列计算。输出未来 7/30 天需求、日均需求和数据可信度；历史不足时显示低可信度或数据不足。

## 本机运行

```bash
go test ./...
go build -o bin/pangu-sales-manager .
./bin/pangu-sales-manager -sync-once
./bin/pangu-sales-manager
```

默认监听 `127.0.0.1:18100`，生产由 Nginx 在 `80` 端口反代。每分钟执行增量同步，保留 5 分钟重叠窗口，并每 24 小时尝试一次全量校准。失败的全量任务不会在下一分钟再次全量执行；连续失败采用最长 30 分钟的指数退避。

Temu 活动价格由同一服务直接拉取并计算，只在进程内保留最新完整快照，不写入 PostgreSQL。启动时立即拉取，默认每 1 分钟刷新一次；服务重启后接口会在首次同步完成前返回 `503`。

活动同步请求 Temu 当前活动报名记录并固定传入 `sessionStatus=2`，随后查询这些活动 SKC 的当前商品状态；不请求全量活动列表或活动详情。返回后会移除非当前嵌套场次及其站点价格，并使用商品状态排除已下架 SKC 或已从 SKC 移除的 SKU。Temu 的 `activityStock` 与 `remainingActivityStock` 位于报名记录层级；同一报名中的多个 SKU 共享这组库存，不能把单次库存差值直接视为某个 SKU 的销量。

报名库存比较和 SKC 推断状态只保存在进程内存中。SKC 首次出现时，如果只有一个活动的 `activityStock - remainingActivityStock` 大于零，就确认该活动；否则标记预警。后续分钟只有一个活动库存下降时确认或切换到该活动，没有下降时延续上一状态，多个活动同时下降时标记预警。对外状态只有 `confirmed` 和 `warning`。

SKU 价格按区间增量保存。SKC 为 `confirmed` 时使用其 `active_enroll_id` 对应的活动价；SKC 为 `warning` 时仅在候选活动的 `dailyPrice` 唯一一致时回退日常价，否则保留 warning 且价格未解析。同一 SKU 的关键价格状态不变时只更新 `update_at`；发生变化时关闭旧区间并以当前分钟创建新行，因此 `start_at`、`update_at`、`end_at` 和持续时间可以恢复价格变化曲线。判断原因只在 warning 区间保留。

## 主要 API

- `GET /api/dashboard?period=day|week|month`
- `GET /api/orders`
- `GET /api/mappings`
- `PATCH /api/mappings/{platform}/{shop}/{platformSKU}`
- `GET /api/warehouses`
- `POST /api/sync`
- `GET /api/sync/status`
- `GET /api/marketing/effective-prices`
- `GET /api/marketing/activity-snapshot`
- `GET /api/marketing/skc-activity-states`
- `GET /api/marketing/sku-price-snapshot?sku_id=&skc_id=&status=`
- `GET /api/marketing/sku-price-snapshot/{skuID}/history?limit=120`
- `GET /api/marketing/sku-current-price?sku_id=...`：从当前进程内存读取。
- `POST /api/marketing/sku-prices/query`：批量查询当前或指定时间的 SKU 价格；不传 `at` 走内存，传 `at` 走 PG 区间并允许最近区间推断。
- `POST /api/marketing/order-price-estimates/backfill`：按时间范围批量预览或写入 Temu 订单价格估算。
- `GET /api/profit/summary`：TEMU 利润源表行数与最近一次导入状态。
- `POST /api/profit/import`：上传 `xlsx` 或 `zip`，按业务键增量覆盖写入本地 PostgreSQL。表单字段：`file`、`shop_key`，单表可附带 `table`；`pending_mode` 为 `merge`（默认增量更新）或 `snapshot`（全量待处理快照）。

TEMU 利润表按 `TEMU利润计算/*.xlsx` 全列入库。发货/退货面单费按文件名区分已出账、待出账、商家仓、第三方仓；账务明细按工作表拆到拒付、履约违规、平台承担面单、处置费和结算流水。再次上传同一业务键会覆盖金额、状态和时间。增量更新保留未出现的旧行；全量快照将本店同类待处理文件中未出现的记录标记为归档，原始记录保留。每个全量快照仅接受一个待处理款项文件和一个待出账面单文件；没有上传的类型不受影响。导入失败整批回滚。

历史查询的 `match_method` 包括 `exact_interval`、`nearest_after`、`nearest_before` 和 `unmatched`。区间外推断始终返回 `warning` 并附带时间距离，不会伪装成准确成交价。订单补全同时保留 `price_source`、`match_method`、订单时间口径和匹配区间，便于审计。

### Restock snapshot identities

`GET /api/restock/snapshot?days=730&version=3` exports the sold SKU (`sales_sku`) separately from the fulfillment SKU (`sku`), with `units`, `conversion_factor` and `warehouse_units`. Aggregate sales by `sales_sku` and procurement demand by `sku`; never attribute unconverted quantities from a different pack to the warehouse SKU's sales. Temu uses its original `ext_code`; Shein's 1:1 opaque aliases retain the existing canonical identity, while non-1:1 conversions retain the source identity. The default version 2 response remains available for existing consumers. Both versions exclude ineligible/cancelled/refunded orders and retain the existing date/status semantics.

## 待结算核销

`GET /api/profit/reconciliation` 返回分页核销明细。参数：`kind=orders|shipping`、`shop_key`、`status=open|all|pending|partial|settled|review|archived`、`q`（单号字面匹配）、`page`、`page_size`（最大 200）。

PO 按店铺、PO、币种分别核销销售回款、运费回款及两类冲回；每类扣减以原始金额为上限，剩余分量为零才算结清。结算流水优先，缺少同 PO 同币种流水时才使用 PO 聚合账单，避免重复扣减。币种或 PO 缺失、折后口径不明和未识别交易类型保留原额，标记待核对。

面单按店铺、包裹、运单、账单类型、币种及金额方向汇总匹配，备注不参与匹配。只使用成功出账记录，部分出账保留余额。核销视图不改动原始金额；归档记录不计入剩余余额。待处理源文件缺少业务日期，不拆分月度余额。未结清 PO 商品使用原单数量和申报金额，不按回款比例折算。

数据库测试需设置隔离的 `TEST_DATABASE_URL`；未设置时跳过财务数据库测试，不连接业务数据库。

## SKU 结算归因

商品回款接口 `GET /api/profit/sku-summary` 支持 `start`、`end`（含结束当天）、`shop_key`、`currency`（默认 USD）。回款和估算金额按所选币种统计，不进行汇率转换；销量按下单日期统计。响应 `attribution` 按店铺及原始币种列出总净额、原始 SKU 净额、匹配补全净额、未归因净额及行数，四项可对账。

原始 SKU 编码优先。编码缺失时，仅使用同店铺唯一 SKU ID 映射，或商品证据完整且只有一个 SKU 的同店铺 PO。证据来自标准订单行、已结算流水及 PO 聚合账单商品。多 SKU、缺少商品编码、映射冲突和缺少 PO 的流水保留未归因，不按金额或数量擅自分摊。归因视图不修改源表，聚合账单只提供商品证据，不重复计入回款。

`GET /api/profit/settlement-attribution` 返回分页交易明细，支持相同日期及店铺参数，以及 `currency`、`status=all|direct|matched|unattributed`、`q`（流水/PO/单据/SKU 字面匹配）、`page`、`page_size`（最大 200）。`GET /api/profit/settlement-attribution/export` 按相同条件导出全部匹配流水，不受分页限制；保留原始金额、币种、到账时间、原始 SKU、归属 SKU 及依据。平台费用和内部成本仍不在此 SKU 净额内。

## 订单费用及公共费用分摊

页面 `?view=profit-ledger` 提供已出账逐笔收支、订单 / SKU 归因、分摊依据、SKU 收支及 CSV。`GET /api/profit/ledger` 和 `/api/profit/ledger/export` 使用同一筛选口径：`start`、`end`（含当天）、`shop_key`、`source`、`currency`、`status`、`q`、`include_undated=1`；查询额外支持分页。原始费用一笔一行，候选订单和商品保存在明细中，不因关联增加收入或费用。按币种核对：已归因净额 + 未归因净额 = 收支净额。

`GET/PUT /api/profit/allocation-policy` 管理全局分摊规则；写入需 `X-Pangu-Request: finance`、同源和当前版本。默认按申报金额比例，也可按数量或暂停自动分摊。计算明细含来源、申报币种、数量、申报价、占比、金额、尾差和版本；规则变更留痕，报表按当前规则重新计算。采购成本已确认 FIFO，但批次扣成本及毛利尚未实现。原商品回款页保持原始 SKU 和唯一匹配口径，公共费用分摊见订单费用页。

包裹读取 Temu `temu_order_details` 中调用成功的包裹及 `temu_shipments` 中 `shipped` 的记录，经 `temu_shipment_orders` 关联 PO。每店独立快照和刷新时间，不依赖销售订单游标；源库故障保留旧快照。完整需求进度见 [需求清单](docs/temu-finance-requirements.md)。

## 订单毛利与财务补录

入口 `?view=profit-margin`、`?view=profit-evidence`。成本、汇率、期初、入出库和商品退回由备货管理，财务补录税金代扣、税金退回、非订单交易费等款项，并维护财务账单 / 税费覆盖确认。四币种下拉、CSV 原子导入、业务键去重、乐观版本和审计适用于全部补录。源流水信息可补费用日期、实际付款日期 / 时间、站点、产品 ID；不覆盖原账单金额。

`GET /api/profit/margin-source` 在只读一致快照中提供订单队列、已去重平台收支、补录及分摊依据，金额和长商品 ID 以字符串传输，不含买家信息。`GET /api/restock/warehouse-evidence` 读取配置的 XLWMS 只读连接，保留无时区源时间；每次最多 5,000 行，需用 kind 和日期缩小范围，唯一订单关联才可导入实际出库。

`GET /api/profit/margin`、`/export` 只读代理备货计算。收入累计到 as_of，三项成本按实际出库扣一次；原币及汇率、费用分摊、退款件数与成本回冲逐笔可查。缺少起始日、期初、出库 / 退款 / 财务覆盖、费用或汇率时保留待核对和空毛利，不将取消数量当退款、不将未知费用补成零。非订单款项单列，不能作为商品毛利。

`GET/POST /api/profit/accounting-records`、`POST /api/profit/accounting-records/remove` 管理补录；写入要求同源 JSON、`X-Pangu-Request: finance` 和版本。`POST /api/profit/margin/snapshots` 由服务端计算所有筛选明细并保存 SHA256 和不可变 JSON，`GET /api/profit/margin/snapshots/{id}` 查看，`?export=1` 导出原快照。快照保存当前缺口状态，后续配置不会改写旧快照，不提供期间锁定或总账过账。

完整功能及正式数据缺口见 [TEMU 财务需求清单](docs/temu-finance-requirements.md)。
