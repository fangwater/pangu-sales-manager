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

Temu 活动价格由同一服务直接拉取并计算，只在进程内保留最新完整快照，不写入 PostgreSQL。启动时立即拉取，默认每 30 分钟刷新一次；服务重启后接口会在首次同步完成前返回 `503`。

活动同步在请求 Temu 报名记录时固定传入 `sessionStatus=2`，只拉取进行中的活动，不请求全量活动列表和活动类型详情。返回后还会移除非当前嵌套场次及其站点价格，内存快照只保留当前活动。Temu 的 `activityStock` 与 `remainingActivityStock` 位于报名记录层级；同一报名中的多个 SKU 共享这组库存，不能把单次库存差值直接视为某个 SKU 的销量。当前接口展示的是“报名总库存减当前剩余库存”的累计差值，尚未持久化相邻快照变化。

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
