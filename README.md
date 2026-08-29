# Pangu Sales Manager

Pangu 销售管理系统将 TEMU、SHEIN 订单与 XLWMS 仓库库存同步到本机 PostgreSQL，统一平台差异后提供销售分析、SKU 预测、仓库视图和映射治理。

## 数据口径

- `normalized_orders`：统一平台、店铺、订单状态、销售时间、仓库归属和原始 JSON。
- `normalized_order_lines`：同时保留平台件数、仓库 SKU、换算系数和仓库换算量。
- `sku_mappings`：平台 SKU 到仓库 SKU 的映射。TEMU 默认使用 `ext_code` 同码映射；SHEIN 使用源库的 `shein_sku_mappings`，缺失时标记为待确认。
- `warehouse_inventory`：XLWMS `integrated` 库存快照，按仓库与仓库 SKU 存储。
- SHEIN 使用平台下单时间；TEMU 当前源表没有下单时间，使用 `first_seen_at` 并在 `occurred_at_source` 明确记录。
- 退款/取消订单不进入销量统计，原始订单仍保留在标准订单库。

## 预测

预测使用最近 7 日与 28 日加权移动平均叠加 28 日线性趋势，按最近 90 天补零序列计算。输出未来 7/30 天需求、日均需求和数据可信度；历史不足时显示低可信度或数据不足。

## 本机运行

```bash
go test ./...
go build -o bin/pangu-sales-manager .
./bin/pangu-sales-manager -sync-once
./bin/pangu-sales-manager
```

默认监听 `127.0.0.1:18100`，生产由 Nginx 在 `80` 端口反代。每分钟执行增量同步，保留 5 分钟重叠窗口，并每 24 小时执行一次全量校准。

Temu 活动价格由同一服务直接拉取并计算，只在进程内保留最新完整快照，不写入 PostgreSQL。启动时立即拉取，默认每 1 分钟刷新一次；服务重启后接口会在首次同步完成前返回 `503`。

活动同步请求 Temu 当前活动报名记录并固定传入 `sessionStatus=2`，随后查询这些活动 SKC 的当前商品状态；不请求全量活动列表或活动详情。返回后会移除非当前嵌套场次及其站点价格，并使用商品状态排除已下架 SKC 或已从 SKC 移除的 SKU。Temu 的 `activityStock` 与 `remainingActivityStock` 位于报名记录层级；同一报名中的多个 SKU 共享这组库存，不能把单次库存差值直接视为某个 SKU 的销量。

每次成功同步都会将报名库存、SKU 站点价格、SKC 推断状态和最终 SKU 解析价格写入 PostgreSQL。SKC 首次出现时，如果只有一个活动的 `activityStock - remainingActivityStock` 大于零，就确认该活动；否则标记预警。后续分钟只有一个活动库存下降时确认或切换到该活动，没有下降时延续上一状态，多个活动同时下降时标记预警。对外状态只有 `confirmed` 和 `warning`，具体原因保存在 `reason`。

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
- `GET /api/marketing/skc-activity-states/{skcID}/history?limit=120`
- `GET /api/marketing/sku-price-snapshot?sku_id=&skc_id=&status=`
- `GET /api/marketing/sku-price-snapshot/{skuID}/history?limit=120`
