const APP_BASE = new URL(".", document.currentScript.src).pathname.replace(/\/$/, "");

const state = {
  view: "overview",
  period: "day",
  platform: "",
  shop: "",
  warehouse: "",
  dashboard: null,
  mappings: null,
  mappingPage: 1,
  orderPage: 1,
  charts: {},
  mappingEditing: null,
  activity: { items: [], meta: {}, page: 1, pageSize: 20, loaded: false, sites: new Map(), types: new Map(), controller: null },
};

const viewMeta = {
  overview: ["销售总览", "TEMU · SHEIN · XLWMS"],
  skus: ["SKU 分析", "销量、库存与需求预测"],
  warehouses: ["仓库库存", "XLWMS 可用库存与销售归属"],
  mappings: ["SKU 映射", "平台 SKU 到仓库 SKU"],
  orders: ["标准订单", "跨平台统一订单结构"],
  "activity-prices": ["活动价格", "TEMU 当前报名活动生效结果"],
};

document.addEventListener("DOMContentLoaded", async () => {
  lucide.createIcons();
  bindEvents();
  const requestedView = new URLSearchParams(window.location.search).get("view");
  await loadWarehouses();
  await loadDashboard();
  if (requestedView && viewMeta[requestedView]) await switchView(requestedView);
});

function bindEvents() {
  document.querySelectorAll("[data-view]").forEach(button => button.addEventListener("click", () => switchView(button.dataset.view)));
  document.querySelectorAll("[data-open-view]").forEach(button => button.addEventListener("click", () => switchView(button.dataset.openView)));
  document.querySelectorAll("[data-period]").forEach(button => button.addEventListener("click", () => {
    state.period = button.dataset.period;
    document.querySelectorAll("[data-period]").forEach(item => item.classList.toggle("active", item === button));
    loadDashboard();
  }));
  document.getElementById("platform-filter").addEventListener("change", event => {
    state.platform = event.target.value;
    filterShopOptions();
    state.shop = document.getElementById("shop-filter").value;
    loadDashboard();
  });
  document.getElementById("shop-filter").addEventListener("change", event => { state.shop = event.target.value; loadDashboard(); });
  document.getElementById("warehouse-filter").addEventListener("change", event => { state.warehouse = event.target.value; loadDashboard(); });
  document.getElementById("reset-filters").addEventListener("click", resetFilters);
  document.getElementById("sync-button").addEventListener("click", startSync);
  document.getElementById("sku-search").addEventListener("input", renderSKUTable);
  document.getElementById("mapping-status-filter").addEventListener("change", debounce(loadMappings, 100));
  document.getElementById("mapping-search").addEventListener("input", debounce(loadMappings, 250));
  document.getElementById("orders-prev").addEventListener("click", () => { if (state.orderPage > 1) { state.orderPage--; loadOrders(); } });
  document.getElementById("orders-next").addEventListener("click", () => { state.orderPage++; loadOrders(); });
  document.getElementById("mapping-form").addEventListener("submit", saveMapping);
  document.getElementById("activity-filter-form").addEventListener("submit", event => { event.preventDefault(); loadActivityPrices(); });
  document.getElementById("activity-reset").addEventListener("click", resetActivityFilters);
  document.getElementById("activity-refresh").addEventListener("click", loadActivityPrices);
  document.getElementById("activity-export").addEventListener("click", exportActivityPrices);
  document.getElementById("activity-prev").addEventListener("click", () => changeActivityPage(-1));
  document.getElementById("activity-next").addEventListener("click", () => changeActivityPage(1));
  document.querySelectorAll("[data-close-dialog]").forEach(button => button.addEventListener("click", () => document.getElementById("mapping-dialog").close()));
}

async function switchView(view) {
  state.view = view;
  document.querySelectorAll("[data-view]").forEach(button => button.classList.toggle("active", button.dataset.view === view));
  document.querySelectorAll(".view").forEach(section => section.classList.toggle("active", section.id === `view-${view}`));
  document.getElementById("page-title").textContent = viewMeta[view][0];
  document.getElementById("page-subtitle").textContent = viewMeta[view][1];
  document.getElementById("global-filters").hidden = view === "mappings" || view === "orders" || view === "activity-prices";
  if (view === "mappings") await loadMappings();
  if (view === "orders") await loadOrders();
  if (view === "warehouses") renderWarehouseChart();
  if (view === "activity-prices" && !state.activity.loaded) await loadActivityPrices();
  updateTopbarForView();

  const activeNavigation = document.querySelector(`[data-view="${view}"]`);
  if (activeNavigation && window.innerWidth <= 820) activeNavigation.scrollIntoView({ block: "nearest", inline: "nearest" });

  const url = new URL(window.location.href);
  if (view === "activity-prices") url.searchParams.set("view", view);
  else url.searchParams.delete("view");
  window.history.replaceState({}, "", url);
}

async function loadActivityPrices() {
  const skcID = cleanPositiveID(document.getElementById("activity-skc-filter").value);
  const skuID = cleanPositiveID(document.getElementById("activity-sku-filter").value);
  const siteID = cleanPositiveID(document.getElementById("activity-site-filter").value);
  const activityType = cleanPositiveID(document.getElementById("activity-type-filter").value);
  if (skcID === null || skuID === null || siteID === null || activityType === null) {
    showError("活动价格筛选条件必须是正整数");
    return;
  }
  const activity = state.activity;
  activity.controller?.abort();
  const controller = new AbortController();
  activity.controller = controller;
  const body = document.getElementById("activity-price-body");
  const refresh = document.getElementById("activity-refresh");
  const search = document.getElementById("activity-search");
  body.innerHTML = emptyRow(10, "正在读取活动报名与库存");
  refresh.classList.add("syncing");
  refresh.disabled = true;
  search.disabled = true;
  try {
    const query = new URLSearchParams({ page: "1", page_size: "1000" });
    if (skcID) query.set("skc_id", skcID);
    if (skuID) query.set("sku_id", skuID);
    if (siteID) query.set("site_id", siteID);
    if (activityType) query.set("activity_type", activityType);
    const first = await api(`/api/marketing/activity-snapshot?${query}`, { signal: controller.signal });
    const items = [...(first.data || [])];
    const total = Number(first.meta?.total || items.length);
    for (let page = 2; page <= Math.ceil(total / 1000); page++) {
      query.set("page", String(page));
      const response = await api(`/api/marketing/activity-snapshot?${query}`, { signal: controller.signal });
      items.push(...(response.data || []));
    }
    activity.items = items;
    activity.meta = { ...(first.meta || {}), total };
    activity.page = 1;
    activity.loaded = true;
    for (const item of items) activity.sites.set(String(item.site_id), item.site_name || `站点 ${item.site_id}`);
    for (const item of items) activity.types.set(String(item.activity_type), item.activity_thematic_name || item.activity_type_name || `活动类型 ${item.activity_type}`);
    populateActivitySites(siteID || "");
    populateActivityTypes(activityType || "");
    renderActivityPrices();
    showError("");
  } catch (error) {
    if (error.name === "AbortError") return;
    activity.items = [];
    activity.loaded = false;
    body.innerHTML = emptyRow(10, error.message);
    setText("activity-result-summary", "活动快照读取失败");
    setText("activity-page-summary", "--");
    document.getElementById("activity-export").disabled = true;
    showError(error.message);
  } finally {
    if (activity.controller === controller) {
      activity.controller = null;
      refresh.classList.remove("syncing");
      refresh.disabled = false;
      search.disabled = false;
    }
  }
}

function renderActivityPrices() {
  const activity = state.activity;
  const stateCounts = activity.meta.state_counts || {};
  const skcCount = new Set(activity.items.map(item => item.skc_id)).size;
  const skuCount = new Set(activity.items.map(item => item.sku_id)).size;
  setText("activity-metric-enrollments", formatNumber(skcCount));
  setText("activity-metric-results", formatNumber(stateCounts.confirmed || 0));
  setText("activity-metric-stock", formatNumber(stateCounts.conflict || 0));
  setText("activity-metric-synced", formatDateTime(activity.meta.synced_at));
  setText("activity-result-summary", `共 ${activity.items.length} 行 · ${skcCount} 个 SKC · ${skuCount} 个 SKU`);
  if (state.view === "activity-prices") setText("updated-at", `活动快照 ${formatDateTime(activity.meta.synced_at)}`);
  document.getElementById("activity-export").disabled = activity.items.length === 0;

  const start = (activity.page - 1) * activity.pageSize;
  const rows = activity.items.slice(start, start + activity.pageSize);
  document.getElementById("activity-price-body").innerHTML = rows.map(activityPriceRow).join("") || emptyRow(10, "当前条件下没有活动报名明细");
  const pageCount = Math.max(1, Math.ceil(activity.items.length / activity.pageSize));
  setText("activity-page", String(activity.page));
  setText("activity-page-summary", `第 ${activity.page} / ${pageCount} 页 · 共 ${activity.items.length} 条`);
  document.getElementById("activity-prev").disabled = activity.page <= 1;
  document.getElementById("activity-next").disabled = activity.page >= pageCount;
  lucide.createIcons();
}

function activityPriceRow(item) {
  const activityLabel = item.activity_thematic_name || item.activity_type_name || `活动类型 ${item.activity_type}`;
  const sessionLabel = item.session_name || `场次 ${item.session_id || "--"}`;
  const timeRange = item.session_start_time && item.session_end_time
    ? `${formatActivityTime(item.session_start_time)} - ${formatActivityTime(item.session_end_time)}` : "--";
  const totalStock = Number(item.activity_stock || 0);
  const remainingStock = Number(item.remaining_activity_stock || 0);
  const consumedStock = Number(item.consumed_activity_stock || 0);
  const stockRate = totalStock > 0 ? Math.max(0, Math.min(100, Math.round(remainingStock / totalStock * 100))) : 0;
  const sharedStock = Number(item.enrollment_sku_count || 0) > 1
    ? `<span class="stock-scope-warning">报名库存共享 ${formatNumber(item.enrollment_sku_count)} 个 SKU</span>` : `<span class="stock-scope-single">单 SKU 报名库存</span>`;
  return `<tr>
    <td class="activity-identity-cell"><span class="sku-code">${escapeHtml(activityLabel)}</span><span class="sku-name">类型 ${escapeHtml(item.activity_type)} · 报名 ${escapeHtml(item.enroll_id)}</span>${sharedStock}</td>
    <td>${activityStatusHTML(item)}</td>
    <td><span class="sku-code">SKC ${escapeHtml(item.skc_id || "--")}</span><span class="sku-name">SKU ${escapeHtml(item.sku_id || "--")}</span></td>
    <td><span class="sku-code">${escapeHtml(item.site_name || `站点 ${item.site_id || "--"}`)}</span><span class="sku-name">${escapeHtml(sessionLabel)} · Site ${escapeHtml(item.site_id || "--")}</span></td>
    <td>${pricePairHTML("站点", item.site_daily_price, item.site_activity_price, item.currency)}</td>
    <td class="stock-cell"><div class="stock-values"><strong>${formatNumber(remainingStock)}</strong><span>/ ${formatNumber(totalStock)}</span></div><div class="stock-track"><i style="width:${stockRate}%"></i></div><small>剩余 / 报名总量</small></td>
    <td>${minuteObservationHTML(item)}</td>
    <td><span class="stock-change ${consumedStock > 0 ? "changed" : ""}">${consumedStock > 0 ? `-${formatNumber(consumedStock)}` : "0"}</span><span class="sku-name">总量 - 当前剩余</span></td>
    <td><span class="session-time">${escapeHtml(timeRange)}</span><span class="sku-name">报名 ${formatActivityTime(item.enroll_time)}</span></td>
    <td>${goodsStatusHTML(item)}</td>
  </tr>`;
}

function activityStatusHTML(item) {
  const status = item.skc_activity_status;
  let badge = '<span class="badge low">状态建立中</span>';
  if (status === "confirmed" && item.selected_effective_activity) badge = '<span class="badge high">已确认生效</span>';
  else if (status === "confirmed") badge = '<span class="badge medium">未选中候选</span>';
  else if (status === "conflict") badge = '<span class="badge insufficient">库存冲突</span>';
  else if (status === "unknown") badge = '<span class="badge low">等待库存证据</span>';
  return `${badge}<span class="state-reason">${escapeHtml(activityStateReason(item.skc_state_reason))}</span><span class="status-secondary">${escapeHtml(sessionStatusLabel(item.session_status))}</span><span class="status-secondary">报名 ${escapeHtml(item.enroll_status)} · 售罄 ${escapeHtml(item.sold_status)}</span>`;
}

function minuteObservationHTML(item) {
  const consumed = Number(item.interval_consumed_stock || 0);
  const increased = Number(item.interval_increased_stock || 0);
  if (consumed > 0) return `<span class="minute-observation consumed">-${formatNumber(consumed)}</span><span class="sku-name">本分钟库存消耗</span>`;
  if (increased > 0) return `<span class="minute-observation increased">+${formatNumber(increased)}</span><span class="sku-name">本分钟库存增加</span>`;
  const baseline = item.previous_remaining_activity_stock == null;
  return `<span class="minute-observation idle">${baseline ? "基线" : "0"}</span><span class="sku-name">${baseline ? "首次观测" : "本分钟无变化"}</span>`;
}

function activityStateReason(reason) {
  return ({
    initial_unique_cumulative_consumption: "起点仅该活动存在累计消耗",
    initial_multiple_cumulative_consumption: "起点多个活动均存在累计消耗",
    initial_no_consumption: "起点没有库存消耗证据",
    interval_unique_consumption: "本分钟仅该活动库存下降",
    interval_multiple_consumption: "本分钟多个活动库存同时下降",
    carry_forward_no_consumption: "本分钟无变化，沿用上一状态",
    carry_forward_conflict: "冲突尚未出现唯一新证据",
    carry_forward_unknown: "仍在等待首次库存消耗",
    no_current_activity: "当前没有活动",
  })[reason] || "等待状态快照";
}

function pricePairHTML(label, daily, activityPrice, currency) {
  const hasPrice = Number(daily || 0) > 0 || Number(activityPrice || 0) > 0;
  if (!hasPrice) return `<div class="price-pair empty"><small>${escapeHtml(label)}层</small><span>API 未返回</span></div>`;
  return `<div class="price-pair"><small>${escapeHtml(label)}层</small><span class="activity-daily-price">${formatMoney(daily, currency)}</span><strong>${formatMoney(activityPrice, currency)}</strong></div>`;
}

function sessionStatusLabel(status) {
  if (Number(status) === 2) return "场次 2 · 进行中";
  if (Number(status) === 3) return "场次 3 · 已结束";
  if (Number(status) === 1) return "场次 1 · 未开始";
  return `场次状态 ${status || "--"}`;
}

function goodsStatusHTML(item) {
  if (!item.goods_record_loaded) return `<span class="badge insufficient">未拉当前商品</span><span class="sku-name">非进行中活动不查询商品</span>`;
  const skcOnShelf = Number(item.skc_site_status) === 1;
  return `<span class="badge ${skcOnShelf ? "high" : "insufficient"}">${skcOnShelf ? "SKC 在售" : `SKC 状态 ${escapeHtml(item.skc_site_status)}`}</span><span class="status-secondary">${item.sku_listed_in_current_goods ? "SKU 当前存在" : "SKU 当前未返回"}</span>`;
}

function populateActivitySites(selected) {
  const select = document.getElementById("activity-site-filter");
  const options = [...state.activity.sites.entries()].sort((left, right) => Number(left[0]) - Number(right[0]));
  select.innerHTML = `<option value="">全部站点</option>${options.map(([id, name]) => `<option value="${escapeHtml(id)}">${escapeHtml(name)} · ${escapeHtml(id)}</option>`).join("")}`;
  select.value = selected;
}

function populateActivityTypes(selected) {
  const select = document.getElementById("activity-type-filter");
  const options = [...state.activity.types.entries()].sort((left, right) => Number(left[0]) - Number(right[0]));
  select.innerHTML = `<option value="">全部活动</option>${options.map(([id, name]) => `<option value="${escapeHtml(id)}">${escapeHtml(name)} · ${escapeHtml(id)}</option>`).join("")}`;
  select.value = selected;
}

function resetActivityFilters() {
  document.getElementById("activity-skc-filter").value = "";
  document.getElementById("activity-sku-filter").value = "";
  document.getElementById("activity-site-filter").value = "";
  document.getElementById("activity-type-filter").value = "";
  loadActivityPrices();
}

function changeActivityPage(delta) {
  const activity = state.activity;
  const pageCount = Math.max(1, Math.ceil(activity.items.length / activity.pageSize));
  activity.page = Math.max(1, Math.min(pageCount, activity.page + delta));
  renderActivityPrices();
}

function exportActivityPrices() {
  const activity = state.activity;
  if (!activity.items.length) return;
  const headers = ["enroll_id", "product_id", "goods_id", "activity_type", "activity_type_name", "activity_thematic_id", "activity_thematic_name", "enroll_status", "sold_status", "enroll_time", "activity_stock", "remaining_activity_stock", "previous_remaining_activity_stock", "interval_consumed_stock", "interval_increased_stock", "consumed_activity_stock", "enrollment_sku_count", "skc_id", "sku_id", "currency", "site_id", "site_name", "site_daily_price", "site_activity_price", "session_id", "session_name", "session_status", "session_start_time", "session_end_time", "skc_activity_status", "skc_active_enroll_id", "selected_effective_activity", "skc_state_reason", "skc_state_started_at", "skc_last_evidence_at", "goods_record_loaded", "skc_site_status", "sku_listed_in_current_goods", "snapshot_synced_at"];
  const rows = activity.items.map(item => [item.enroll_id, item.product_id, item.goods_id, item.activity_type, item.activity_type_name,
    item.activity_thematic_id, item.activity_thematic_name, item.enroll_status, item.sold_status, item.enroll_time,
    item.activity_stock, item.remaining_activity_stock, item.previous_remaining_activity_stock, item.interval_consumed_stock,
    item.interval_increased_stock, item.consumed_activity_stock, item.enrollment_sku_count,
    item.skc_id, item.sku_id, item.currency, item.site_id, item.site_name,
    Number(item.site_daily_price || 0) / 100, Number(item.site_activity_price || 0) / 100, item.session_id, item.session_name,
    item.session_status, item.session_start_time, item.session_end_time, item.skc_activity_status, item.skc_active_enroll_id,
    item.selected_effective_activity, item.skc_state_reason, item.skc_state_started_at, item.skc_last_evidence_at,
    item.goods_record_loaded, item.skc_site_status,
    item.sku_listed_in_current_goods, activity.meta.synced_at || ""]);
  const csv = [headers, ...rows].map(row => row.map(csvCell).join(",")).join("\n");
  const link = document.createElement("a");
  link.href = URL.createObjectURL(new Blob([`\ufeff${csv}`], { type: "text/csv;charset=utf-8" }));
  link.download = `temu-activity-prices-${new Date().toISOString().slice(0, 10)}.csv`;
  link.click();
  URL.revokeObjectURL(link.href);
}

function cleanPositiveID(value) {
  const normalized = String(value || "").trim();
  if (!normalized) return "";
  return /^\d+$/.test(normalized) && normalized !== "0" ? normalized : null;
}

function formatMoney(cents, currency = "USD") {
  const amount = Number(cents || 0) / 100;
  return currency === "USD" ? new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(amount) : `${escapeHtml(currency)} ${amount.toFixed(2)}`;
}
function formatActivityTime(value) { return value ? new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(Number(value))) : "--"; }
function csvCell(value) { const text = String(value ?? ""); return /[",\n]/.test(text) ? `"${text.replaceAll('"', '""')}"` : text; }

function updateTopbarForView() {
  const activityView = state.view === "activity-prices";
  document.getElementById("sync-button").hidden = activityView;
  if (activityView) {
    setText("updated-at", `活动快照 ${formatDateTime(state.activity.meta.synced_at)}`);
  } else if (state.dashboard) {
    setText("updated-at", `更新于 ${formatDateTime(state.dashboard.generated_at)}`);
  }
}

async function loadWarehouses() {
  try {
    const response = await api("/api/warehouses");
    const select = document.getElementById("warehouse-filter");
    response.data.forEach(warehouse => {
      const option = document.createElement("option");
      option.value = warehouse.code;
      option.textContent = `${warehouse.code} · ${warehouse.name}`;
      select.appendChild(option);
    });
  } catch (error) {
    showError(error.message);
  }
}

async function loadDashboard() {
  setDashboardLoading(true);
  const params = new URLSearchParams({ period: state.period });
  if (state.platform) params.set("platform", state.platform);
  if (state.shop) params.set("shop", state.shop);
  if (state.warehouse) params.set("warehouse", state.warehouse);
  try {
    const response = await api(`/api/dashboard?${params}`);
    state.dashboard = response.data;
    renderDashboard();
    showError("");
  } catch (error) {
    showError(error.message);
    setSourceStatus("error", "数据读取失败");
  } finally {
    setDashboardLoading(false);
  }
}

function renderDashboard() {
  const data = state.dashboard;
  const summary = data.summary;
  setText("metric-orders", formatNumber(summary.orders));
  setText("metric-units", formatNumber(summary.warehouse_units));
  setText("metric-skus", formatNumber(summary.active_skus));
  setText("metric-stock", formatNumber(summary.available_stock));
  setText("metric-coverage", `${formatNumber(data.mapping_quality.coverage_pct)}%`);
  setText("metric-range", `${data.range.start} 至 ${data.range.end}`);
  const growth = document.getElementById("metric-growth");
  growth.textContent = `${summary.period_growth_pct >= 0 ? "+" : ""}${formatNumber(summary.period_growth_pct)}% 环比`;
  growth.className = summary.period_growth_pct >= 0 ? "positive" : "negative";
  setText("metric-mapping-detail", `${data.mapping_quality.verified} 已确认 · ${data.mapping_quality.inferred} 待确认`);
  setText("summary-period-label", ({ day: "近 14 日仓库换算销量", week: "近 12 周仓库换算销量", month: "近 6 月仓库换算销量" })[state.period]);
  if (state.view !== "activity-prices") setText("updated-at", `更新于 ${formatDateTime(data.generated_at)}`);
  renderSignals();
  renderSalesChart();
  renderPlatformChart();
  renderOverviewSKUs();
  renderSKUTable();
  renderWarehouses();
  renderWarehouseChart();
  renderSourceStatus();
}

function renderSalesChart() {
  const data = state.dashboard.series;
  destroyChart("sales");
  state.charts.sales = new Chart(document.getElementById("sales-chart"), {
    type: "bar",
    data: {
      labels: data.map(item => item.label),
      datasets: [
        { type: "line", label: "仓库换算量", data: data.map(item => item.warehouse_units), borderColor: "#d25a43", backgroundColor: "rgba(210,90,67,.07)", pointBackgroundColor: "#ffffff", pointBorderColor: "#d25a43", pointBorderWidth: 2, pointRadius: 2, pointHoverRadius: 4, borderWidth: 2, tension: .22, fill: true, yAxisID: "units" },
        { type: "bar", label: "订单数", data: data.map(item => item.orders), backgroundColor: "rgba(73,107,145,.34)", borderRadius: 1, barPercentage: .56, yAxisID: "orders" },
      ],
    },
    options: chartOptions(true),
  });
}

function renderPlatformChart() {
  const rows = state.dashboard.platforms;
  const colors = ["#d25a43", "#496b91", "#ad7b2c", "#286b59"];
  destroyChart("platform");
  document.getElementById("platform-legend").innerHTML = rows.map((item, index) => `<div class="platform-bar-row"><span>${escapeHtml(item.label)}</span><div class="platform-track"><i class="platform-fill" style="width:${Math.max(2, item.share_pct)}%;background:${colors[index % colors.length]}"></i></div><strong>${formatNumber(item.share_pct)}%</strong></div>`).join("");
}

function renderSignals() {
  const skus = state.dashboard.skus;
  const top = skus[0];
  const fastest = [...skus].filter(item => item.warehouse_units >= 5 && item.prior_units >= 5).sort((a, b) => b.growth_pct - a.growth_pct)[0];
  const tightest = [...skus].filter(item => item.days_of_cover != null && item.days_of_cover >= 0).sort((a, b) => a.days_of_cover - b.days_of_cover)[0];
  setText("signal-top-sku", top ? `${top.warehouse_sku} · ${formatNumber(top.warehouse_units)}` : "--");
  setText("signal-growth-sku", fastest ? `${fastest.warehouse_sku} · +${formatNumber(fastest.growth_pct)}%` : "--");
  setText("signal-cover-sku", tightest ? `${tightest.warehouse_sku} · ${formatNumber(tightest.days_of_cover)} 天` : "--");

  const priorities = [...skus].sort((a, b) => {
    const scoreA = a.forecast.next_30_days - a.available_stock;
    const scoreB = b.forecast.next_30_days - b.available_stock;
    return scoreB - scoreA;
  }).slice(0, 4);
  document.getElementById("demand-signal-list").innerHTML = priorities.map((item, index) => `<div class="demand-row">
    <span class="demand-rank">${String(index + 1).padStart(2, "0")}</span>
    <div class="demand-copy"><strong>${escapeHtml(item.warehouse_sku)}</strong><small>库存 ${formatNumber(item.available_stock)} · 可售 ${item.days_of_cover == null ? "--" : `${formatNumber(item.days_of_cover)} 天`}</small></div>
    <div class="demand-value"><strong>${formatNumber(item.forecast.next_30_days)}</strong><small>30 天需求</small></div>
  </div>`).join("") || `<div class="demand-row"><div class="demand-copy"><strong>暂无需求信号</strong></div></div>`;
}

function renderOverviewSKUs() {
  const body = document.getElementById("overview-sku-body");
  body.innerHTML = state.dashboard.skus.slice(0, 8).map(sku => `<tr>
    <td class="sku-cell"><span class="sku-code">${escapeHtml(sku.warehouse_sku)}</span><span class="sku-name">${escapeHtml(sku.product_name || "--")}</span></td>
    <td class="num">${formatNumber(sku.warehouse_units)}</td>
    <td class="num">${growthHTML(sku.growth_pct)}</td>
    <td class="num">${formatNumber(sku.available_stock)}</td>
    <td class="num">${sku.days_of_cover == null ? "--" : `${formatNumber(sku.days_of_cover)} 天`}</td>
    <td class="num">${formatNumber(sku.forecast.next_30_days)}</td>
    <td>${confidenceBadge(sku.forecast.confidence)}</td>
  </tr>`).join("") || emptyRow(7, "当前筛选没有销售记录");
}

function renderSKUTable() {
  if (!state.dashboard) return;
  const query = document.getElementById("sku-search").value.trim().toLowerCase();
  const rows = state.dashboard.skus.filter(sku => `${sku.warehouse_sku} ${sku.product_name}`.toLowerCase().includes(query));
  setText("sku-count-label", `${rows.length} 个活跃 SKU`);
  document.getElementById("sku-table-body").innerHTML = rows.map(sku => `<tr>
    <td><span class="sku-code">${escapeHtml(sku.warehouse_sku)}</span></td>
    <td><span class="sku-name">${escapeHtml(sku.product_name || "--")}</span></td>
    <td class="num">${formatNumber(sku.platform_units)}</td>
    <td class="num">${formatNumber(sku.warehouse_units)}</td>
    <td class="num">${formatNumber(sku.forecast.next_7_days)}</td>
    <td class="num">${formatNumber(sku.forecast.next_30_days)}</td>
    <td class="num">${formatNumber(sku.forecast.daily_run_rate)}</td>
    <td class="num">${formatNumber(sku.available_stock)}</td>
    <td class="num">${sku.days_of_cover == null ? "--" : formatNumber(sku.days_of_cover)}</td>
    <td>${confidenceBadge(sku.forecast.confidence)}</td>
  </tr>`).join("") || emptyRow(10, "没有匹配的 SKU");
}

function renderWarehouses() {
  const rows = state.dashboard.warehouses;
  document.getElementById("warehouse-grid").innerHTML = rows.map(item => `<article class="warehouse-card"><header><h3>${escapeHtml(item.name || item.code)}</h3><span>${escapeHtml(item.code)}</span></header><strong>${formatNumber(item.available_stock)}</strong><footer>${item.active_sku_count} 个库存 SKU · 本期销售 ${formatNumber(item.warehouse_units)}</footer></article>`).join("");
}

function renderWarehouseChart() {
  if (!state.dashboard || state.view !== "warehouses") return;
  const rows = state.dashboard.warehouses;
  destroyChart("warehouse");
  state.charts.warehouse = new Chart(document.getElementById("warehouse-chart"), {
    type: "bar",
    data: { labels: rows.map(item => item.code), datasets: [
      { label: "可用库存", data: rows.map(item => item.available_stock), backgroundColor: "rgba(73,107,145,.72)", borderRadius: 1, yAxisID: "stock" },
      { label: "本期销售", data: rows.map(item => item.warehouse_units), backgroundColor: "rgba(210,90,67,.72)", borderRadius: 1, yAxisID: "sales" },
    ] },
    options: chartOptions(false),
  });
}

async function loadMappings() {
  const params = new URLSearchParams({ page: "1", page_size: "100" });
  const status = document.getElementById("mapping-status-filter").value;
  const query = document.getElementById("mapping-search").value.trim();
  if (status) params.set("status", status);
  if (query) params.set("q", query);
  try {
    const response = await api(`/api/mappings?${params}`);
    state.mappings = response.data;
    renderMappings();
  } catch (error) { showError(error.message); }
}

function renderMappings() {
  const data = state.mappings;
  setText("mapping-summary", `${data.total} 条映射`);
  document.getElementById("warehouse-sku-options").innerHTML = data.warehouse_sku_options.map(sku => `<option value="${escapeHtml(sku)}"></option>`).join("");
  document.getElementById("mapping-table-body").innerHTML = data.items.map((item, index) => `<tr>
    <td><strong>${escapeHtml(item.platform.toUpperCase())}</strong><span class="sku-name">${escapeHtml(item.shop_key)}</span></td>
    <td><span class="sku-code">${escapeHtml(item.platform_sku)}</span></td>
    <td><span class="sku-code">${escapeHtml(item.warehouse_sku)}</span></td>
    <td class="num">${formatNumber(item.conversion_factor)}</td>
    <td>${mappingBadge(item.mapping_status)}</td>
    <td>${escapeHtml(item.mapping_source)}</td>
    <td><button class="row-command" data-edit-mapping="${index}" title="编辑映射" aria-label="编辑映射"><i data-lucide="pencil"></i></button></td>
  </tr>`).join("") || emptyRow(7, "没有匹配的映射");
  document.querySelectorAll("[data-edit-mapping]").forEach(button => button.addEventListener("click", () => openMappingDialog(data.items[Number(button.dataset.editMapping)])));
  lucide.createIcons();
}

function openMappingDialog(item) {
  state.mappingEditing = item;
  setText("mapping-dialog-source", `${item.platform.toUpperCase()} · ${item.shop_key} · ${item.platform_sku}`);
  document.getElementById("mapping-warehouse-sku").value = item.warehouse_sku;
  document.getElementById("mapping-factor").value = item.conversion_factor;
  document.getElementById("mapping-dialog").showModal();
}

async function saveMapping(event) {
  event.preventDefault();
  if (!state.mappingEditing) return;
  const item = state.mappingEditing;
  const payload = {
    warehouse_sku: document.getElementById("mapping-warehouse-sku").value.trim(),
    conversion_factor: Number(document.getElementById("mapping-factor").value),
  };
  try {
    await api(`/api/mappings/${encodeURIComponent(item.platform)}/${encodeURIComponent(item.shop_key)}/${encodeURIComponent(item.platform_sku)}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    document.getElementById("mapping-dialog").close();
    await Promise.all([loadMappings(), loadDashboard()]);
  } catch (error) { showError(error.message); }
}

async function loadOrders() {
  const params = new URLSearchParams({ page: String(state.orderPage), page_size: "30" });
  if (state.platform) params.set("platform", state.platform);
  if (state.shop) params.set("shop", state.shop);
  try {
    const response = await api(`/api/orders?${params}`);
    const data = response.data;
    if (state.orderPage > 1 && data.items.length === 0) { state.orderPage--; return loadOrders(); }
    setText("order-summary", `${formatNumber(data.total)} 个标准订单`);
    setText("orders-page", String(data.page));
    document.getElementById("orders-table-body").innerHTML = data.items.map(order => {
      const units = order.lines.reduce((total, line) => total + Number(line.warehouse_quantity), 0);
      return `<tr><td><span class="badge ${order.platform === "temu" ? "medium" : "high"}">${escapeHtml(order.platform.toUpperCase())}</span></td><td><span class="sku-code">${escapeHtml(order.order_no)}</span><span class="sku-name">${escapeHtml(order.shop_key)}</span></td><td>${formatDateTime(order.occurred_at)}</td><td>${escapeHtml(order.normalized_status)}</td><td>${escapeHtml(order.warehouse_code || "待归属")}</td><td class="num">${order.lines.length}</td><td class="num">${formatNumber(units)}</td><td>${timeSourceLabel(order.occurred_at_source)}</td></tr>`;
    }).join("") || emptyRow(8, "当前页没有订单");
  } catch (error) { showError(error.message); }
}

async function startSync() {
  const button = document.getElementById("sync-button");
  button.classList.add("syncing");
  try {
    await api("/api/sync", { method: "POST" });
    setSourceStatus("pending", "正在同步三套数据源");
    await pollSync();
    await Promise.all([loadWarehousesOnce(), loadDashboard()]);
  } catch (error) {
    if (!error.message.includes("sync already running")) showError(error.message);
    await pollSync();
    await loadDashboard();
  } finally { button.classList.remove("syncing"); }
}

async function pollSync() {
  for (let attempt = 0; attempt < 90; attempt++) {
    await wait(2000);
    const response = await api("/api/sync/status");
    if (!response.data.running) return;
  }
  throw new Error("同步等待超时");
}

function renderSourceStatus() {
  const sync = state.dashboard.sync;
  if (sync.status === "failed") setSourceStatus("error", "最近同步失败");
  else if (sync.status === "running") setSourceStatus("pending", "正在同步");
  else setSourceStatus("ok", sync.completed_at ? `${sync.mode === "incremental" ? "增量" : "全量"}同步于 ${formatClock(sync.completed_at)}` : "等待首次同步");
}

function setSourceStatus(kind, message) {
  const root = document.getElementById("source-status");
  root.querySelector(".status-dot").className = `status-dot ${kind === "ok" ? "" : kind}`;
  root.querySelector("small").textContent = message;
}

function filterShopOptions() {
  const select = document.getElementById("shop-filter");
  [...select.options].forEach(option => { option.hidden = Boolean(option.dataset.platform && state.platform && option.dataset.platform !== state.platform); });
  const selected = select.selectedOptions[0];
  if (selected?.hidden) select.value = "";
}

function resetFilters() {
  state.period = "day"; state.platform = ""; state.shop = ""; state.warehouse = "";
  document.querySelectorAll("[data-period]").forEach(button => button.classList.toggle("active", button.dataset.period === "day"));
  document.getElementById("platform-filter").value = "";
  document.getElementById("shop-filter").value = "";
  document.getElementById("warehouse-filter").value = "";
  filterShopOptions();
  loadDashboard();
}

function chartOptions(dualAxis) {
  const scales = {
    x: { grid: { display: false }, ticks: { color: "#68736f", maxRotation: 0, autoSkip: true } },
    units: { beginAtZero: true, position: "left", grid: { color: "#edf0ef" }, ticks: { color: "#68736f" } },
  };
  if (dualAxis) scales.orders = { beginAtZero: true, position: "right", grid: { display: false }, ticks: { color: "#68736f", precision: 0 } };
  else scales.stock = scales.units, scales.sales = { beginAtZero: true, position: "right", grid: { display: false }, ticks: { color: "#68736f" } }, delete scales.units;
  return { responsive: true, maintainAspectRatio: false, interaction: { mode: "index", intersect: false }, plugins: { legend: { display: false }, tooltip: { backgroundColor: "#17201d", padding: 10, titleFont: { size: 12 }, bodyFont: { size: 11 } } }, scales };
}

function destroyChart(key) { if (state.charts[key]) { state.charts[key].destroy(); delete state.charts[key]; } }
function setDashboardLoading(loading) { document.getElementById("sync-button").disabled = loading; }
function setText(id, value) { document.getElementById(id).textContent = value; }
function formatNumber(value) { return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format(Number(value || 0)); }
function formatDateTime(value) { return value ? new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(value)) : "--"; }
function formatClock(value) { return value ? new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(value)) : "--"; }
function growthHTML(value) { const className = value >= 0 ? "positive" : "negative"; return `<span class="change ${className}">${value >= 0 ? "+" : ""}${formatNumber(value)}%</span>`; }
function confidenceBadge(value) { const labels = { high: "高", medium: "中", low: "低", insufficient: "不足" }; return `<span class="badge ${value}">${labels[value] || value}</span>`; }
function mappingBadge(value) { const labels = { mapped: "已映射", identity: "同码", inferred: "待确认", manual: "人工", unmapped: "未映射" }; return `<span class="badge ${value}">${labels[value] || value}</span>`; }
function timeSourceLabel(value) { return ({ platform_order_time: "平台下单时间", list_order_time: "列表下单时间", first_seen: "首次发现时间" })[value] || value; }
function emptyRow(columns, message) { return `<tr class="empty-row"><td colspan="${columns}">${escapeHtml(message)}</td></tr>`; }
function escapeHtml(value) { return String(value ?? "").replace(/[&<>'"]/g, character => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" })[character]); }
function showError(message) { document.getElementById("alert-region").innerHTML = message ? `<div class="alert">${escapeHtml(message)}</div>` : ""; }
function wait(ms) { return new Promise(resolve => setTimeout(resolve, ms)); }
function debounce(fn, delay) { let timer; return (...args) => { clearTimeout(timer); timer = setTimeout(() => fn(...args), delay); }; }
async function loadWarehousesOnce() { return Promise.resolve(); }

async function api(path, options = {}) {
  const response = await fetch(`${APP_BASE}${path}`, options);
  let payload;
  try { payload = await response.json(); } catch { throw new Error(`请求失败 (${response.status})`); }
  if (!response.ok || !payload.success) throw new Error(payload.error || `请求失败 (${response.status})`);
  return payload;
}
