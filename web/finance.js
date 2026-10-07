const financeColors = ["#4f46e5", "#a5b4fc", "#8b5cf6", "#fb923c", "#38bdf8", "#cbd5e1"];
const financeShopName = key => ({ "panda-homes": "Panda Homes", "panda-buy": "Panda Buy" })[key] || key;
const financeMoney = value => value == null ? "—" : new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 2 }).format(Number(value));
const financePercent = value => value == null ? "—" : `${Number(value).toFixed(2)}%`;
const financeDate = value => value ? new Intl.DateTimeFormat("sv-SE", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit" }).format(new Date(value)) : "—";
const financeN = (rows, key) => rows.reduce((sum, row) => sum + Number(row[key] || 0), 0);
const financePill = (text, kind = "muted") => `<span class="finance-pill ${kind}">${escapeHtml(text)}</span>`;
const financeCurrencyOK = status => (status?.sources || []).every(source => source.currency === "USD");

function bindFinanceReports() {
  document.getElementById("finance-summary-form").addEventListener("submit", event => { event.preventDefault(); loadProfitDailySummary(); });
  document.getElementById("profit-summary-shop").addEventListener("change", event => { state.profitSummary.shopKey = event.target.value; loadProfitDailySummary(); });
  document.querySelectorAll("[data-summary-period]").forEach(button => button.addEventListener("click", () => {
    state.profitSummary.period = button.dataset.summaryPeriod;
    document.querySelectorAll("[data-summary-period]").forEach(item => item.classList.toggle("active", item === button));
    if (state.profitSummary.data) renderProfitDailySummary();
  }));
  document.getElementById("finance-range").addEventListener("change", () => { financeSetDateRange(); if (state.profitSummary.data) renderProfitDailySummary(); });
  ["finance-start", "finance-end"].forEach(id => document.getElementById(id).addEventListener("change", () => {
    document.getElementById("finance-range").value = "custom";
    if (financeDatesValid("finance-start", "finance-end") && state.profitSummary.data) renderProfitDailySummary();
  }));
  document.getElementById("finance-show-detail").addEventListener("change", renderProfitDailySummary);
  document.getElementById("finance-export").addEventListener("click", exportFinanceSummary);
  document.getElementById("finance-sku-form").addEventListener("submit", event => { event.preventDefault(); loadProfitSKUSummary(); });
  document.getElementById("profit-sku-refresh").addEventListener("click", loadProfitSKUSummary);
  document.getElementById("finance-sku-keyword").addEventListener("input", () => { state.profitSKU.search = document.getElementById("finance-sku-keyword").value; renderProfitSKUSummary(); });
  document.getElementById("finance-sku-sort").addEventListener("change", event => { state.profitSKU.sort = event.target.value; renderProfitSKUSummary(); });
  document.getElementById("finance-sku-export").addEventListener("click", exportFinanceSKU);
  document.getElementById("profit-unsettled-shop").addEventListener("change", event => { state.profitUnsettled.shopKey = event.target.value; loadProfitUnsettledSummary(); });
  document.getElementById("profit-unsettled-refresh").addEventListener("click", loadProfitUnsettledSummary);
  document.getElementById("finance-pending-keyword").addEventListener("input", renderProfitUnsettledSummary);
  document.getElementById("finance-pending-export").addEventListener("click", exportFinancePending);
}

function financeDatesValid(startID, endID) {
  const start = document.getElementById(startID).value, end = document.getElementById(endID).value;
  if (start && end && start > end) { showError("开始日期不能晚于结束日期。"); return false; }
  showError("");
  return true;
}

function financeSetDateRange() {
  const report = state.profitSummary, preset = document.getElementById("finance-range").value;
  if (preset === "custom") return;
  const today = financeDate(report.status?.generated_at || new Date());
  let start = report.data?.range.start || today, end = report.data?.range.end || today;
  if (preset === "ledger") {
    const sources = (report.status?.sources || []).filter(row => !["unsettled", "shipping_pending"].includes(row.source));
    const first = sources.map(row => row.first_at).filter(Boolean).sort()[0], last = sources.map(row => row.last_at).filter(Boolean).sort().at(-1);
    if (first && last) { start = financeDate(first); end = financeDate(last); }
  }
  if (preset === "month") { start = today.slice(0, 7) + "-01"; end = today; }
  if (preset === "30") { const d = new Date(today + "T00:00:00Z"); d.setUTCDate(d.getUTCDate() - 29); start = d.toISOString().slice(0, 10); end = today; }
  if (preset === "previous") { const d = new Date(today.slice(0, 7) + "-01T00:00:00Z"); d.setUTCDate(0); end = d.toISOString().slice(0, 10); start = end.slice(0, 7) + "-01"; }
  document.getElementById("finance-start").value = start;
  document.getElementById("finance-end").value = end;
}

async function financeLoad(key, buttonID, noticeID, exportID, paths, render) {
  const report = state[key];
  report.controller?.abort();
  const controller = new AbortController(); report.controller = controller;
  const button = document.getElementById(buttonID);
  button.disabled = true; button.classList.add("syncing");
  document.getElementById(exportID).disabled = true;
  document.getElementById(noticeID).setAttribute("aria-busy", "true");
  try {
    const responses = await Promise.all(paths.map(path => api(path, { signal: controller.signal })));
    if (report.controller !== controller) return;
    report.data = responses[0].data; report.status = responses[1].data; report.loaded = true;
    render(); showError("");
    document.getElementById(exportID).disabled = false;
    updateTopbarForView();
  } catch (error) {
    if (error.name === "AbortError" || report.controller !== controller) return;
    report.loaded = false; report.data = null; report.status = null;
    render();
    financeNotice(noticeID, "报表加载失败", "本次数据未取得，请刷新后重试。", "error");
    showError(error.message);
  } finally {
    if (report.controller === controller) { button.disabled = false; button.classList.remove("syncing"); document.getElementById(noticeID).removeAttribute("aria-busy"); }
    lucide.createIcons();
  }
}

async function loadProfitDailySummary() {
  if (!financeDatesValid("finance-start", "finance-end")) return;
  const params = new URLSearchParams({ period: "day", shop_key: state.profitSummary.shopKey });
  await financeLoad("profitSummary", "profit-summary-refresh", "finance-notice", "finance-export", [`/api/profit/daily-summary?${params}`, `/api/profit/report-status?${params}`], () => { financeSetDateRange(); renderProfitDailySummary(); });
}

function financeNotice(id, title, detail, kind = "info") {
  const element = document.getElementById(id);
  element.className = `finance-notice ${kind}`;
  element.innerHTML = `<i data-lucide="${kind === "error" ? "circle-alert" : "info"}"></i><div><strong>${escapeHtml(title)}</strong>${detail ? `<span>${escapeHtml(detail)}</span>` : ""}</div>`;
  lucide.createIcons();
}

function financeMetric(label, value, note, icon, kind = "", series = []) {
  const values = series.filter(value => value != null && Number.isFinite(Number(value))).map(Number);
  let spark = "";
  if (values.length > 1) {
    const min = Math.min(...values), range = Math.max(...values) - min || 1;
    const points = values.map((value, i) => `${i * 130 / (values.length - 1)},${33 - (value - min) / range * 26}`).join(" ");
    spark = `<svg class="metric-spark" viewBox="0 0 130 40" aria-hidden="true"><polyline points="${points}"/></svg>`;
  }
  return `<article class="finance-metric ${kind}"><div class="metric-top"><span>${escapeHtml(label)}</span><i data-lucide="${icon}"></i></div><strong>${escapeHtml(value)}</strong><small>${escapeHtml(note)}</small>${spark}</article>`;
}

function financeSelectedRows() {
  return FinanceCore.groupDaily(state.profitSummary.data?.rows || [], state.profitSummary.period, document.getElementById("finance-start").value, document.getElementById("finance-end").value);
}

function renderProfitDailySummary() {
  const report = state.profitSummary, rows = financeSelectedRows(), totals = FinanceCore.sumRows(rows), status = report.status;
  const currencyOK = financeCurrencyOK(status), ledger = FinanceCore.hasLedger(totals) && currencyOK;
  document.getElementById("finance-summary-metrics").innerHTML = [
    financeMetric("平台收支净额", financeMoney(ledger ? totals.platform_balance_amount : null), "未扣采购、头程等内部成本", "wallet", "featured", rows.map(row => FinanceCore.hasLedger(row) ? row.platform_balance_amount : null)),
    financeMetric("结算回款净额", financeMoney(totals.settled_rows && currencyOK ? totals.payback_amount : null), "销售与运费回款，已减冲回", "arrow-down-left"),
    financeMetric("平台费用净支出", financeMoney(totals.fee_rows && currencyOK ? -totals.known_fee_balance_amount : null), "面单、违规、拒付及处置，含补偿", "receipt-text"),
    financeMetric("已到账销售回款", financeMoney(totals.settled_rows && currencyOK ? totals.sales_receipt_amount : null), "销售回款原额，未减销售冲回", "shopping-bag"),
  ].join("");
  const sources = status?.sources || [], settled = sources.filter(row => row.source === "settled");
  const latest = settled.map(row => row.last_at).filter(Boolean).sort().at(-1);
  const missingShops = (status?.coverage || []).filter(row => !settled.some(source => source.shop_key === row.shop_key)).map(row => financeShopName(row.shop_key));
  const detail = missingShops.length ? `${missingShops.join("、")} 未导入财务账单，合计仅含已有账单` : "USD · 按到账 / 记账时间汇总";
  financeNotice("finance-notice", !report.data ? "等待财务数据" : !currencyOK ? "账单含其他或未知币种，暂停 USD 合计" : !ledger ? "所选期间缺少账单" : `结算账单截至 ${financeDate(latest)}`, !ledger && report.data ? "暂无金额不表示收入为零" : detail, currencyOK ? "info" : "error");
  const start = document.getElementById("finance-start").value, end = document.getElementById("finance-end").value;
  setText("profit-summary-range", `${start || "—"} — ${end || "—"} · ${financeShopName(report.shopKey) || "全部 TEMU 店铺"}`);
  setText("finance-row-count", `${rows.length} 个期间`);
  setText("finance-order-note", `${formatNumber(totals.units)} 件 · 估算仅包含 ${formatNumber(totals.estimated_sales_matched)} 个已定价订单行`);
  renderFinanceSummaryTable(rows, totals, currencyOK);
  renderFinanceCharts(rows, totals, currencyOK);
  if (state.view === "profit-summary") updateTopbarForView();
  lucide.createIcons();
}

function financeAmountCell(value, available = true, note = "") {
  const shown = available && value != null;
  const kind = !shown ? "money-muted" : Number(value) < 0 ? "money-negative" : Number(value) > 0 ? "money-positive" : "";
  return `<td class="num ${kind}">${shown ? financeMoney(value) : "—"}${note ? `<span class="cell-caption">${escapeHtml(note)}</span>` : ""}</td>`;
}

function renderFinanceSummaryTable(rows, totals, currencyOK) {
  const detailed = document.getElementById("finance-show-detail").checked;
  const headers = ["期间", "销量", "销售回款", "运费回款", "两类冲回", "平台费用收支", "收支净额", "估算销售额"];
  const fields = [["发货面单", "shipping_label_fee_amount"], ["商家仓退货", "return_label_fee_merchant_amount"], ["第三方仓退货", "return_label_fee_third_party_amount"], ["延迟到货", "fulfillment_delay_amount"], ["虚假发货", "fulfillment_false_ship_amount"], ["买家拒付", "buyer_chargeback_amount"], ["处置费", "disposal_fee_amount"], ["平台补偿", "platform_return_label_fee_amount"]];
  if (detailed) headers.push(...fields.map(field => field[0]), "销售冲回笔数", "运费冲回笔数", "取消订单");
  document.getElementById("finance-summary-head").innerHTML = `<tr>${headers.map((label, index) => `<th${index ? ' class="num"' : ""}>${label}</th>`).join("")}</tr>`;
  const renderRow = (row, total = false) => {
    const settled = currencyOK && row.settled_rows > 0, fees = currencyOK && row.fee_rows > 0, ledger = currencyOK && FinanceCore.hasLedger(row);
    let html = `<td>${total ? "合计" : escapeHtml(row.label)}${!total && state.profitSummary.period === "week" ? `<span class="cell-caption">${row.first_date.slice(5)} — ${row.last_date.slice(5)}</span>` : ""}</td><td class="num">${formatNumber(row.units)}</td>`;
    html += financeAmountCell(row.sales_receipt_amount, settled) + financeAmountCell(row.freight_receipt_amount, settled) + financeAmountCell(row.sales_chargeback_amount + row.freight_chargeback_amount, settled);
    html += financeAmountCell(row.known_fee_balance_amount, fees) + financeAmountCell(row.platform_balance_amount, ledger) + financeAmountCell(row.estimated_sales_amount, currencyOK && row.estimated_sales_matched > 0, row.estimated_sales_matched ? `${formatNumber(row.estimated_sales_matched)} 行` : "");
    if (detailed) html += fields.map(([, key]) => financeAmountCell(row[key], fees)).join("") + [row.sales_chargebacks, row.freight_chargebacks, row.refund_orders].map(value => `<td class="num">${formatNumber(value)}</td>`).join("");
    return `<tr>${html}</tr>`;
  };
  document.getElementById("profit-summary-body").innerHTML = rows.slice().reverse().map(row => renderRow(row)).join("") || emptyRow(headers.length, "所选期间没有订单或账单");
  document.getElementById("profit-summary-foot").innerHTML = rows.length ? renderRow(totals, true) : "";
}

function financeChartOptions(extra = {}) {
  return { responsive: true, maintainAspectRatio: false, animation: false, interaction: { intersect: false, mode: "index" }, layout: { padding: { top: 8 } },
    plugins: { legend: { position: "bottom", labels: { color: "#64748b", boxWidth: 8, boxHeight: 8, usePointStyle: true, padding: 22, font: { size: 12 } } }, tooltip: { backgroundColor: "#172033", padding: 13, titleFont: { size: 12 }, bodyFont: { size: 12 }, callbacks: { label: item => `${item.dataset.label}: ${financeMoney(item.raw)}` } } },
    scales: { x: { grid: { display: false }, border: { display: false }, ticks: { color: "#64748b", maxTicksLimit: 8, maxRotation: 0, font: { size: 12 } } }, y: { beginAtZero: true, grid: { color: "#eef1f6" }, border: { display: false }, ticks: { color: "#64748b", maxTicksLimit: 5, font: { size: 12 }, callback: value => Math.abs(value) >= 1000 ? `${Number(value / 1000).toFixed(0)}k` : value } } }, ...extra };
}

function financeChart(key, id, configuration) {
  destroyChart(key);
  state.charts[key] = new Chart(document.getElementById(id), configuration);
}

function renderFinanceCharts(rows, totals, currencyOK) {
  const labels = rows.map(row => state.profitSummary.period === "week" ? row.first_date.slice(5) : row.bucket.slice(5)), available = currencyOK && FinanceCore.hasLedger(totals);
  financeChart("financeCash", "finance-cash-chart", { type: "bar", data: { labels, datasets: [
    { type: "line", label: "结算回款净额", data: rows.map(row => currencyOK && row.settled_rows ? row.payback_amount : null), borderColor: "#818cf8", backgroundColor: "#818cf81a", tension: .28, borderWidth: 2, pointRadius: rows.length > 35 ? 0 : 3, pointBackgroundColor: "#fff", spanGaps: false, fill: true },
    { type: "bar", label: "平台费用收支", data: rows.map(row => currencyOK && row.fee_rows ? row.known_fee_balance_amount : null), backgroundColor: "#fb923c80", borderRadius: 4, maxBarThickness: 25 },
    { type: "line", label: "收支净额", data: rows.map(row => currencyOK && FinanceCore.hasLedger(row) ? row.platform_balance_amount : null), borderColor: "#4f46e5", tension: .28, borderWidth: 3, pointRadius: rows.length > 35 ? 0 : 3, pointBackgroundColor: "#4f46e5", spanGaps: false },
  ] }, options: financeChartOptions() });
  document.getElementById("finance-cash-empty").hidden = available;
  document.getElementById("finance-cash-empty").textContent = currencyOK ? "所选期间没有对应账单" : "账单币种待核对";
  const expenses = [["发货面单", Math.max(0, -totals.shipping_label_fee_amount)], ["退货面单", Math.max(0, -totals.return_label_fee_merchant_amount) + Math.max(0, -totals.return_label_fee_third_party_amount)], ["履约违规", Math.abs(totals.fulfillment_delay_amount) + Math.abs(totals.fulfillment_false_ship_amount)], ["拒付", Math.abs(totals.buyer_chargeback_amount)], ["处置费", Math.abs(totals.disposal_fee_amount)]].filter(([, amount]) => amount > 0 && currencyOK);
  const expenseTotal = expenses.reduce((sum, [, amount]) => sum + amount, 0);
  financeDonut("financeExpenses", "finance-expense-chart", expenses);
  document.getElementById("finance-expense-empty").hidden = expenses.length > 0;
  document.getElementById("finance-expense-total").innerHTML = expenseTotal ? `<small>支出合计</small><strong>${financeShortMoney(expenseTotal)}</strong>` : "";
  document.getElementById("finance-expense-legend").innerHTML = financeLegend(expenses);
  document.getElementById("finance-expense-comp").innerHTML = currencyOK && totals.fee_rows ? `<span>平台退货补偿</span><strong>${financeMoney(totals.platform_return_label_fee_amount)}</strong>` : "";
  financeWaterfall("finance-waterfall-chart", "financeWaterfall", totals, available);
  document.getElementById("finance-waterfall-empty").hidden = available;
  document.getElementById("finance-formula-strip").innerHTML = available ? `<span>净结算 <strong>${financeMoney(totals.payback_amount)}</strong></span><i>＋</i><span>平台费用收支 <strong>${financeMoney(totals.known_fee_balance_amount)}</strong></span><i>＝</i><span class="result">收支净额 <strong>${financeMoney(totals.platform_balance_amount)}</strong></span>` : "";
  financeChart("financeOrders", "finance-coverage-chart", { type: "bar", data: { labels, datasets: [{ label: "订单销量", data: rows.map(row => row.units), backgroundColor: "#c7d2fe", borderRadius: 4, maxBarThickness: 26, yAxisID: "y" }, { type: "line", label: "已定价行估算销售额", data: rows.map(row => currencyOK && row.estimated_sales_matched ? row.estimated_sales_amount : null), borderColor: "#4f46e5", borderWidth: 2, pointRadius: 3, yAxisID: "amount" }] }, options: financeChartOptions({ scales: { x: { grid: { display: false }, ticks: { color: "#64748b", maxRotation: 0 } }, y: { beginAtZero: true, grid: { color: "#eef1f6" }, title: { display: true, text: "件" } }, amount: { beginAtZero: true, position: "right", grid: { display: false }, title: { display: true, text: "USD" } } }, plugins: { legend: { position: "bottom", labels: { usePointStyle: true } }, tooltip: { callbacks: { label: item => `${item.dataset.label}: ${item.dataset.yAxisID === "amount" ? financeMoney(item.raw) : formatNumber(item.raw)}` } } } }) });
}

async function loadProfitSKUSummary() {
  if (!financeDatesValid("profit-sku-start", "profit-sku-end")) return;
  const report = state.profitSKU;
  report.start = document.getElementById("profit-sku-start").value; report.end = document.getElementById("profit-sku-end").value;
  report.shopKey = document.getElementById("profit-sku-shop").value;
  // First visit follows the full available report range, rather than silently
  // showing a recent empty month while the only imported bills are older.
  if (!report.start && !report.end) {
    try {
      const daily = await api(`/api/profit/daily-summary?shop_key=${encodeURIComponent(report.shopKey)}&period=month`);
      report.start = daily.data.range.start; report.end = daily.data.range.end;
    } catch (error) { showError(error.message); return; }
  }
  const params = new URLSearchParams({ shop_key: report.shopKey });
  if (report.start) params.set("start", report.start);
  if (report.end) params.set("end", report.end);
  await financeLoad("profitSKU", "profit-sku-search", "finance-sku-notice", "finance-sku-export", [`/api/profit/sku-summary?${params}`, `/api/profit/report-status?${params}`], () => {
    if (report.data) { document.getElementById("profit-sku-start").value = report.data.range.start; document.getElementById("profit-sku-end").value = report.data.range.end; }
    renderProfitSKUSummary();
  });
}

function financeSKURows() {
  const report = state.profitSKU, keyword = (report.search || "").trim().toLowerCase();
  return (report.data?.rows || []).filter(row => !keyword || `${row.platform_sku} ${row.sku_name}`.toLowerCase().includes(keyword)).slice().sort((a, b) => report.sort === "coverage" ? (FinanceCore.coverage(a) ?? -1) - (FinanceCore.coverage(b) ?? -1) || a.platform_sku.localeCompare(b.platform_sku) : Number(b[report.sort] || 0) - Number(a[report.sort] || 0) || a.platform_sku.localeCompare(b.platform_sku));
}

function renderProfitSKUSummary() {
  const report = state.profitSKU, rows = financeSKURows(), all = report.data?.rows || [], totals = FinanceCore.sumRows(all), currencyOK = financeCurrencyOK(report.status), hasSales = all.some(row => row.settled_rows > 0);
  document.getElementById("finance-sku-metrics").innerHTML = [financeMetric("已到账销售回款", financeMoney(hasSales && currencyOK ? totals.sales_receipt_amount : null), "商品销售回款原额", "circle-dollar-sign", "featured"), financeMetric("已归因结算净额", financeMoney(hasSales && currencyOK ? totals.payback_amount : null), "仅含带商品标识的结算", "wallet"), financeMetric("商品销量", formatNumber(totals.units), `${formatNumber(all.length)} 个商品 SKU`, "package"), financeMetric("估算销售额", financeMoney(currencyOK && totals.estimated_sales_matched ? totals.estimated_sales_amount : null), `仅 ${formatNumber(totals.estimated_sales_matched)} 个已定价订单行`, "calculator")].join("");
  financeNotice("finance-sku-notice", "商品回款按 SKU 汇总", currencyOK ? "运费与订单费用尚未完整分摊，商品净额未扣内部成本" : "账单含其他或未知币种，暂停 USD 合计");
  setText("profit-sku-range", `${report.data?.range.start || "—"} — ${report.data?.range.end || "—"} · ${rows.length} 个 SKU`);
  document.getElementById("profit-sku-body").innerHTML = rows.map(row => `<tr><td><span class="finance-sku-code">${escapeHtml(row.platform_sku)}</span><span class="finance-product" title="${escapeHtml(row.sku_name)}">${escapeHtml(row.sku_name || "暂无名称")}</span></td><td class="num">${formatNumber(row.units)}</td>${financeAmountCell(row.sales_receipt_amount, currencyOK && row.settled_rows > 0)}${financeAmountCell(row.sales_chargeback_amount, currencyOK && row.settled_rows > 0)}${financeAmountCell(row.payback_amount, currencyOK && row.settled_rows > 0)}${financeAmountCell(row.estimated_sales_amount, currencyOK && row.estimated_sales_matched > 0)}<td class="num">${formatNumber(row.estimated_sales_matched)} / ${formatNumber(row.estimated_sales_total)}</td><td class="num">${formatNumber(row.refund_orders)}</td></tr>`).join("") || emptyRow(8, report.loaded ? "没有匹配的 SKU，请调整搜索或日期" : "报表数据暂不可用");
  const sorted = all.filter(row => row.sales_receipt_amount > 0 && currencyOK).slice().sort((a, b) => b.sales_receipt_amount - a.sales_receipt_amount), top = sorted.slice(0, 10);
  financeChart("financeSKU", "finance-sku-chart", { type: "bar", data: { labels: top.map(row => row.platform_sku), datasets: [{ label: "销售回款", data: top.map(row => row.sales_receipt_amount), backgroundColor: top.map((_, index) => index === 0 ? "#4f46e5" : "#a5b4fc"), borderRadius: 5, maxBarThickness: 20 }] }, options: financeChartOptions({ indexAxis: "y", plugins: { legend: { display: false }, tooltip: { callbacks: { label: item => financeMoney(item.raw) } } }, scales: { x: { grid: { color: "#eef1f6" }, border: { display: false }, ticks: { color: "#64748b", font: { size: 12 } } }, y: { grid: { display: false }, border: { display: false }, ticks: { color: "#475569", font: { size: 11 }, callback: function(value) { const label = this.getLabelForValue(value); return label.length > 30 ? label.slice(0, 28) + "…" : label; } } } } }) });
  document.getElementById("finance-sku-empty").hidden = top.length > 0;
  const mix = sorted.slice(0, 4).map(row => [row.platform_sku, row.sales_receipt_amount]), remaining = financeN(sorted.slice(4), "sales_receipt_amount");
  if (remaining) mix.push(["其他商品", remaining]);
  financeDonut("financeSKUMix", "finance-sku-mix-chart", mix);
  document.getElementById("finance-sku-mix-total").innerHTML = hasSales && currencyOK ? `<small>销售回款</small><strong>${financeShortMoney(totals.sales_receipt_amount)}</strong>` : '<small>暂无回款</small>';
  document.getElementById("finance-sku-mix-legend").innerHTML = financeLegend(mix);
  lucide.createIcons();
}

async function loadProfitUnsettledSummary() {
  const params = new URLSearchParams({ shop_key: state.profitUnsettled.shopKey });
  await financeLoad("profitUnsettled", "profit-unsettled-refresh", "finance-pending-notice", "finance-pending-export", [`/api/profit/unsettled-summary?${params}`, `/api/profit/report-status?${params}`], renderProfitUnsettledSummary);
}

function renderProfitUnsettledSummary() {
  const report = state.profitUnsettled, shops = report.data?.shops || [], checks = report.status?.pending || [], currencyOK = financeCurrencyOK(report.status);
  const overlaps = financeN(checks, "overlap_orders"), overlapAmount = financeN(checks, "overlap_net_amount"), net = financeN(checks, "net_amount"), orderCount = financeN(checks, "orders"), labelRows = financeN(checks, "label_rows"), discountedRows = financeN(checks, "discounted_rows");
  document.getElementById("finance-pending-metrics").innerHTML = [financeMetric("快照净待回款", financeMoney(orderCount && currencyOK && !discountedRows ? net : null), discountedRows ? "折后回款列的口径待确认" : "未自动核销的原始快照", "hourglass", "featured"), financeMetric("待处理 PO", formatNumber(orderCount), "导入快照中的订单", "receipt-text"), financeMetric("待出账面单费", financeMoney(labelRows && currencyOK ? Math.abs(financeN(checks, "label_amount")) : null), `${formatNumber(labelRows)} 条面单记录`, "truck"), financeMetric("重叠待回款", financeMoney(overlaps && currencyOK && !discountedRows ? overlapAmount : null), `${formatNumber(overlaps)} 个 PO 已有到账记录`, "files")].join("");
  const imported = (report.status?.sources || []).filter(row => row.source === "unsettled").map(row => row.imported_at).filter(Boolean).sort().at(-1);
  financeNotice("finance-pending-notice", `快照导入 ${financeDate(imported)}`, !currencyOK ? "其他或未知币种，暂停 USD 合计" : discountedRows ? "含非零折后回款列，暂停净额合计" : `${formatNumber(overlaps)} 个重叠 PO 未自动核销，重叠金额不可再次累计`);
  const values = [financeN(shops, "sales_receipt"), financeN(shops, "freight_receipt"), -Math.abs(financeN(shops, "sales_chargeback")), -Math.abs(financeN(shops, "freight_chargeback"))];
  financeChart("financePendingCash", "finance-pending-cash-chart", { type: "bar", data: { labels: ["销售待回款", "运费待回款", "销售冲回", "运费冲回"], datasets: [{ label: "快照金额", data: currencyOK && orderCount ? values : [], backgroundColor: ["#4f46e5", "#a5b4fc", "#fb923c", "#fbbf24"], borderRadius: 6, maxBarThickness: 65 }] }, options: financeChartOptions({ plugins: { legend: { display: false }, tooltip: { callbacks: { label: item => financeMoney(item.raw) } } } }) });
  financeDonut("financePendingOverlap", "finance-pending-overlap-chart", [["与已到账重叠", overlaps], ["其余待处理", Math.max(0, orderCount - overlaps)]], "count");
  document.getElementById("finance-pending-overlap-total").innerHTML = `<small>重叠 PO</small><strong>${formatNumber(overlaps)}</strong>`;
  document.getElementById("finance-pending-overlap-legend").innerHTML = [["与已到账重叠", overlaps], ["其余待处理", Math.max(0, orderCount - overlaps)]].map(([label, count], index) => `<div><i style="background:${financeColors[index]}"></i><span>${label}</span><strong>${formatNumber(count)} 个</strong></div>`).join("");
  document.getElementById("profit-unsettled-shop-body").innerHTML = shops.map(shop => { const check = checks.find(row => row.shop_key === shop.shop_key) || {}; return `<tr><td>${escapeHtml(financeShopName(shop.shop_key))}</td>${financeAmountCell(shop.sales_receipt, currencyOK)}${financeAmountCell(-Math.abs(shop.sales_chargeback), currencyOK)}${financeAmountCell(shop.freight_receipt, currencyOK)}${financeAmountCell(-Math.abs(shop.freight_chargeback), currencyOK)}${financeAmountCell(Number(shop.sales_receipt_after_discount || 0) + Number(shop.freight_receipt_after_discount || 0), currencyOK)}${financeAmountCell(shop.pending_shipping_label_fee, currencyOK)}<td class="num">${formatNumber(check.overlap_orders)}</td>${financeAmountCell(check.overlap_net_amount, currencyOK && !discountedRows)}</tr>`; }).join("") || emptyRow(9, "所选店铺没有待结算快照");
  const keyword = document.getElementById("finance-pending-keyword").value.trim().toLowerCase(), all = report.data?.skus || [], declared = financeN(all, "declared_total");
  const rows = all.filter(row => !keyword || `${row.sku_id} ${row.sku_name}`.toLowerCase().includes(keyword)).slice().sort((a, b) => b.declared_total - a.declared_total);
  document.getElementById("profit-unsettled-sku-body").innerHTML = rows.map(row => `<tr><td>${escapeHtml(financeShopName(row.shop_key))}</td><td><span class="finance-sku-code">${escapeHtml(row.sku_id)}</span><span class="finance-product" title="${escapeHtml(row.sku_name)}">${escapeHtml(row.sku_name)}</span></td><td class="num">${formatNumber(row.quantity)}</td>${financeAmountCell(row.declared_total, currencyOK)}<td>${financePercent(declared && currencyOK ? row.declared_total / declared * 100 : null)}<div class="cell-coverage"><i style="width:${declared && currencyOK ? Math.max(0, row.declared_total / declared * 100) : 0}%"></i></div></td></tr>`).join("") || emptyRow(5, "没有匹配的待处理 SKU");
  lucide.createIcons();
}
function financeDownloadCSV(name, headers, rows, notes) {
  const lines = [headers, ...rows];
  if (notes) lines.push([], ["报表口径", notes]);
  const csv = lines.map(row => row.map(FinanceCore.csvCell).join(",")).join("\r\n");
  const url = URL.createObjectURL(new Blob(["\ufeff" + csv], { type: "text/csv;charset=utf-8" })), link = document.createElement("a");
  link.href = url; link.download = name; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function exportFinanceSummary() {
  if (!state.profitSummary.loaded || !financeDatesValid("finance-start", "finance-end")) return;
  const currencyOK = financeCurrencyOK(state.profitSummary.status);
  const amountFields = [
    ["销售回款_USD", "sales_receipt_amount", "settled"], ["运费回款_USD", "freight_receipt_amount", "settled"],
    ["销售冲回_USD", "sales_chargeback_amount", "settled"], ["运费冲回_USD", "freight_chargeback_amount", "settled"],
    ["发货面单费_USD", "shipping_label_fee_amount", "fee"], ["退货面单商家仓_USD", "return_label_fee_merchant_amount", "fee"],
    ["退货面单第三方仓_USD", "return_label_fee_third_party_amount", "fee"], ["延迟到货费_USD", "fulfillment_delay_amount", "fee"],
    ["虚假发货费_USD", "fulfillment_false_ship_amount", "fee"], ["买家拒付_USD", "buyer_chargeback_amount", "fee"],
    ["处置费_USD", "disposal_fee_amount", "fee"], ["平台退货补偿_USD", "platform_return_label_fee_amount", "fee"],
  ];
  const rows = financeSelectedRows().map(row => [row.label, row.units, currencyOK && row.settled_rows ? row.payback_amount : "", currencyOK && row.fee_rows ? row.known_fee_balance_amount : "", currencyOK && FinanceCore.hasLedger(row) ? row.platform_balance_amount : "", currencyOK && row.estimated_sales_matched ? row.estimated_sales_amount : "", FinanceCore.coverage(row) == null ? "" : Number(FinanceCore.coverage(row).toFixed(4)), row.estimated_sales_matched, row.estimated_sales_total, row.refund_orders, row.sales_chargebacks, row.freight_chargebacks, ...amountFields.map(([, key, source]) => currencyOK && (source === "settled" ? row.settled_rows : row.fee_rows) ? row[key] : ""), "", "", "", "", ""]);
  financeDownloadCSV(`temu-finance-${state.profitSummary.shopKey || "all"}-${document.getElementById("finance-start").value}-${document.getElementById("finance-end").value}.csv`, ["期间", "销量", "结算回款净额_USD", "其他费用收支_USD", "平台收支净额_USD", "估算销售额_USD", "价格覆盖率_%", "已定价订单行", "总订单行", "取消订单数", "销售冲回笔数", "运费冲回笔数", ...amountFields.map(([label]) => label), "税金代扣_未接入", "税金退回_未接入", "非订单交易费_未接入", "毛利润_未接入", "毛利率_未接入"], rows, "仅已导入账单，空白表示未取得数据。未扣采购、头程、仓储等内部成本，不能作为利润。订单时间为系统订单日期，费用按到账/记账日期。估算仅包含已定价行。");
}

function exportFinanceSKU() {
  if (!state.profitSKU.loaded) return;
  const currencyOK = financeCurrencyOK(state.profitSKU.status);
  financeDownloadCSV(`temu-sku-finance-${state.profitSKU.data.range.start}-${state.profitSKU.data.range.end}.csv`, ["SKU", "商品", "销量", "估算销售额_USD", "已定价行", "总订单行", "销售回款_USD", "销售冲回_USD", "已归因结算净额_USD", "取消订单行"], financeSKURows().map(row => [row.platform_sku, row.sku_name, row.units, currencyOK && row.estimated_sales_matched ? row.estimated_sales_amount : "", row.estimated_sales_matched, row.estimated_sales_total, currencyOK && row.settled_rows > 0 ? row.sales_receipt_amount : "", currencyOK && row.settled_rows > 0 ? row.sales_chargeback_amount : "", currencyOK && row.settled_rows > 0 ? row.payback_amount : "", row.refund_orders]), "所选日期和搜索条件。订单级运费与费用未完整分摊，商品成本未接入，已归因净额不代表利润。");
}

function exportFinancePending() {
  if (!state.profitUnsettled.loaded) return;
  const currencyOK = financeCurrencyOK(state.profitUnsettled.status);
  financeDownloadCSV("temu-pending-check.csv", ["店铺", "币种", "待处理PO", "原始净待回款", "待出账面单行", "待出账面单费", "与已到账重叠PO", "重叠净待回款", "与已出账重叠面单行"], (state.profitUnsettled.status.pending || []).map(row => [financeShopName(row.shop_key), row.currency, row.orders, currencyOK && !row.discounted_rows ? row.net_amount : "", row.label_rows, currencyOK ? row.label_amount : "", row.overlap_orders, currencyOK && !row.discounted_rows ? row.overlap_net_amount : "", row.posted_overlap_rows]), "导入快照，未自动核销。重叠可能涉及部分结算，必须核对；回款与面单未按订单对齐，不能相减作为利润。非零折后列需确认含义后计算。");
}

function financeWaterfall(id, key, totals, available) {
  const deltas = [totals.sales_receipt_amount || 0, totals.freight_receipt_amount || 0, (totals.sales_chargeback_amount || 0) + (totals.freight_chargeback_amount || 0), totals.known_fee_balance_amount || 0, totals.platform_balance_amount || 0];
  let running = 0;
  const points = deltas.map((amount, index) => { if (index === deltas.length - 1) return [0, amount]; const start = running; running += amount; return [start, running]; });
  const values = { id: "cashBridgeValues", afterDatasetsDraw(chart) { if (!available) return; const meta = chart.getDatasetMeta(0); chart.ctx.save(); chart.ctx.font = chart.width < 520 ? "600 11px sans-serif" : "600 12px sans-serif"; chart.ctx.fillStyle = "#475569"; chart.ctx.textAlign = "center"; meta.data.forEach((bar, index) => { const label = chart.width < 520 ? financeShortMoney(deltas[index]) : financeMoney(deltas[index]); const half = chart.ctx.measureText(label).width / 2; const x = Math.max(half + 4, Math.min(chart.width - half - 4, bar.x)); chart.ctx.fillText(label, x, Math.min(bar.y, bar.base) - 10); }); chart.ctx.restore(); } };
  financeChart(key, id, { type: "bar", plugins: [values], data: { labels: window.innerWidth <= 560 ? ["销售", "运费", "冲回", "费用", "净额"] : ["销售回款", "运费回款", "销售 / 运费冲回", "平台费用收支", "收支净额"], datasets: [{ label: "金额", data: available ? points : [], backgroundColor: ["#818cf8", "#a5b4fc", "#fb923c", "#fbbf24", "#4f46e5"], borderRadius: 5, maxBarThickness: 68 }] }, options: financeChartOptions({ layout: { padding: { top: 32 } }, plugins: { legend: { display: false }, tooltip: { callbacks: { label: item => financeMoney(deltas[item.dataIndex]) } } } }) });
}

function financeShortMoney(value) { return Math.abs(value) >= 1000 ? `${value < 0 ? "−" : ""}$${(Math.abs(value) / 1000).toFixed(1)}k` : financeMoney(value); }

function financeDonut(key, id, items, unit = "money") {
  financeChart(key, id, { type: "doughnut", data: { labels: items.map(item => item[0]), datasets: [{ data: items.map(item => item[1]), backgroundColor: financeColors, borderWidth: 3, borderColor: "#fff", hoverOffset: 4 }] }, options: { responsive: true, maintainAspectRatio: false, animation: false, cutout: "78%", plugins: { legend: { display: false }, tooltip: { callbacks: { label: item => `${item.label}: ${unit === "count" ? formatNumber(item.raw) + " 个" : financeMoney(item.raw)}` } } } } });
}

function financeLegend(items) {
  return items.map(([label, amount], index) => `<div><i style="background:${financeColors[index % financeColors.length]}"></i><span title="${escapeHtml(label)}">${escapeHtml(label)}</span><strong>${financeMoney(amount)}</strong></div>`).join("") || '<div class="finance-no-data">暂无对应数据</div>';
}
