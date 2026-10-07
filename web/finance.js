const financeColors = ["#4e7860", "#83a99a", "#688db1", "#c49a6d", "#b7c38b", "#b88371", "#a4b4a2"];
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

function financeNotice(id, title, detail, kind = "warning") {
  const element = document.getElementById(id);
  element.className = `finance-notice ${kind}`;
  element.innerHTML = `<i data-lucide="${kind === "error" ? "circle-alert" : kind === "info" ? "info" : "triangle-alert"}"></i><div><strong>${escapeHtml(title)}</strong><p>${escapeHtml(detail)}</p></div>`;
  lucide.createIcons();
}

function financeMetric(label, value, note, icon, kind = "") {
  return `<article class="finance-metric ${kind}"><div class="metric-top"><span>${escapeHtml(label)}</span><i data-lucide="${icon}"></i></div><strong>${escapeHtml(value)}</strong><small>${escapeHtml(note)}</small></article>`;
}

function financeSelectedRows() {
  return FinanceCore.groupDaily(state.profitSummary.data?.rows || [], state.profitSummary.period, document.getElementById("finance-start").value, document.getElementById("finance-end").value);
}

function renderProfitDailySummary() {
  const report = state.profitSummary, rows = financeSelectedRows(), totals = FinanceCore.sumRows(rows), status = report.status;
  const currencyOK = financeCurrencyOK(status), ledger = FinanceCore.hasLedger(totals) && currencyOK;
  const priced = totals.estimated_sales_matched > 0 && currencyOK;
  document.getElementById("finance-summary-metrics").innerHTML = [
    financeMetric("平台收支净额", financeMoney(ledger ? totals.platform_balance_amount : null), "已导入账单 · 未扣内部成本", "wallet", "featured"),
    financeMetric("结算回款净额", financeMoney(totals.settled_rows && currencyOK ? totals.payback_amount : null), "销售 + 运费回款 − 冲回", "arrow-down-left"),
    financeMetric("其他费用净支出", financeMoney(totals.fee_rows && currencyOK ? -totals.known_fee_balance_amount : null), "面单 / 违规 / 拒付 / 处置，含补偿", "receipt-text"),
    financeMetric("价格估算覆盖", financePercent(FinanceCore.coverage(totals)), `${formatNumber(totals.estimated_sales_matched)} / ${formatNumber(totals.estimated_sales_total)} 个订单行`, "scan-line", "warning"),
    financeMetric("毛利润 / 毛利率", "待接入", "采购、头程及仓储等成本未接入", "calculator", "unavailable"),
  ].join("");
  const sources = status?.sources || [], settled = sources.filter(x => x.source === "settled");
  const latest = settled.map(x => x.last_at).filter(Boolean).sort().at(-1);
  const missingShops = (status?.coverage || []).filter(x => !settled.some(s => s.shop_key === x.shop_key)).map(x => financeShopName(x.shop_key));
  const details = [];
  if (latest) details.push(`已导入结算账单截至 ${financeDate(latest)}，之后的空白不代表零收入。`);
  else details.push("所选店铺还没有已导入的结算流水。");
  if (missingShops.length) details.push(`${missingShops.join("、")} 未导入财务账单，合计仅含已有数据。`);
  if (!priced && totals.estimated_sales_total) details.push("所选期间没有回填价格，估算销售额暂不可用。");
  else if (priced) details.push(`推断销售额 ${financeMoney(totals.estimated_sales_amount)} 仅覆盖已定价订单行。`);
  if (!currencyOK) details.push("源账单含非 USD 或未知币种，金额合计已暂停展示，请先按币种核对。");
  const title = !report.data ? "等待财务数据" : !ledger ? "所选期间缺少可用账单，暂不能评估财务表现" : "当前为已导入账单的收支，利润数据仍待补齐";
  financeNotice("finance-notice", title, details.join(" "));
  const start = document.getElementById("finance-start").value, end = document.getElementById("finance-end").value;
  setText("profit-summary-range", `${start || "—"} 至 ${end || "—"} · ${financeShopName(report.shopKey) || "全部 TEMU 店铺"}`);
  setText("finance-row-count", `${rows.length} 个统计期间`);
  renderFinanceSummaryTable(rows, totals, currencyOK);
  renderFinanceCharts(rows, totals, currencyOK);
  renderFinanceQuality(status);
  lucide.createIcons();
}

function financeAmountCell(value, available = true, note = "") {
  const shown = available && value != null;
  const kind = !shown ? "money-muted" : Number(value) < 0 ? "money-negative" : Number(value) > 0 ? "money-positive" : "";
  return `<td class="num ${kind}">${shown ? financeMoney(value) : "—"}${note ? `<span class="cell-caption">${escapeHtml(note)}</span>` : ""}</td>`;
}

function financeCoverageCell(row) {
  const ratio = FinanceCore.coverage(row);
  return `<td>${financePercent(ratio)}<span class="cell-caption">${formatNumber(row.estimated_sales_matched)} / ${formatNumber(row.estimated_sales_total)} 行</span><div class="cell-coverage"><i style="width:${Math.min(100, Math.max(0, ratio || 0))}%"></i></div></td>`;
}

function renderFinanceSummaryTable(rows, totals, currencyOK) {
  const detailed = document.getElementById("finance-show-detail").checked;
  const headers = ["期间", "销量", "结算回款净额", "其他费用收支", "平台收支净额", "估算销售额", "价格覆盖", "取消订单", "销售冲回笔数", "运费冲回笔数"];
  const fields = [
    ["销售回款", "sales_receipt_amount", "settled"], ["运费回款", "freight_receipt_amount", "settled"],
    ["销售冲回", "sales_chargeback_amount", "settled"], ["运费冲回", "freight_chargeback_amount", "settled"],
    ["发货面单费", "shipping_label_fee_amount", "fee"], ["退货面单 · 商家仓", "return_label_fee_merchant_amount", "fee"],
    ["退货面单 · 第三方仓", "return_label_fee_third_party_amount", "fee"], ["延迟到货", "fulfillment_delay_amount", "expense"],
    ["虚假发货", "fulfillment_false_ship_amount", "expense"], ["买家拒付", "buyer_chargeback_amount", "expense"],
    ["处置费", "disposal_fee_amount", "fee"], ["平台退货补偿", "platform_return_label_fee_amount", "fee"],
  ];
  if (detailed) headers.push(...fields.map(x => x[0]));
  document.getElementById("finance-summary-head").innerHTML = `<tr>${headers.map((h, i) => `<th${i ? ' class="num"' : ""}>${escapeHtml(h)}</th>`).join("")}</tr>`;
  const rowHTML = (row, total = false) => {
    const settlement = currencyOK && row.settled_rows > 0, fees = currencyOK && row.fee_rows > 0, ledger = currencyOK && FinanceCore.hasLedger(row);
    let cells = `<td>${total ? "合计" : escapeHtml(row.label)}${!total && state.profitSummary.period === "week" ? `<span class="cell-caption">${row.first_date.slice(5)} — ${row.last_date.slice(5)}</span>` : ""}${!total && !ledger ? '<span class="cell-caption">无导入账单</span>' : ""}</td><td class="num">${formatNumber(row.units)}</td>`;
    cells += financeAmountCell(row.payback_amount, settlement) + financeAmountCell(row.known_fee_balance_amount, fees) + financeAmountCell(row.platform_balance_amount, ledger);
    cells += financeAmountCell(row.estimated_sales_amount, currencyOK && row.estimated_sales_matched > 0) + financeCoverageCell(row);
    cells += [row.refund_orders, row.sales_chargebacks, row.freight_chargebacks].map(v => `<td class="num">${formatNumber(v)}</td>`).join("");
    if (detailed) cells += fields.map(([, key, kind]) => financeAmountCell(kind === "expense" ? -Math.abs(row[key]) : row[key], kind === "settled" ? settlement : fees)).join("");
    return `<tr>${cells}</tr>`;
  };
  document.getElementById("profit-summary-body").innerHTML = rows.slice().reverse().map(row => rowHTML(row)).join("") || emptyRow(headers.length, "所选期间没有订单或账单数据");
  document.getElementById("profit-summary-foot").innerHTML = rows.length ? rowHTML(totals, true) : "";
}

function financeChartOptions(extra = {}) {
  return {
    responsive: true, maintainAspectRatio: false, animation: false, interaction: { intersect: false, mode: "index" },
    plugins: { legend: { position: "bottom", labels: { color: "#7c8b80", boxWidth: 7, boxHeight: 7, usePointStyle: true, padding: 18, font: { size: 10 } } }, tooltip: { backgroundColor: "#294b38", padding: 12, titleFont: { size: 11 }, bodyFont: { size: 11 }, callbacks: { label: item => `${item.dataset.label}: ${financeMoney(item.raw)}` } } },
    scales: { x: { grid: { display: false }, border: { display: false }, ticks: { color: "#91a08f", maxTicksLimit: 8, maxRotation: 0, font: { size: 10 } } }, y: { grid: { color: "#edf1e8" }, border: { display: false }, ticks: { color: "#91a08f", maxTicksLimit: 5, font: { size: 10 }, callback: value => Math.abs(value) >= 1000 ? `${Number(value / 1000).toFixed(0)}k` : value } } }, ...extra,
  };
}

function financeChart(key, id, configuration) {
  destroyChart(key);
  state.charts[key] = new Chart(document.getElementById(id), configuration);
}

function renderFinanceCharts(rows, totals, currencyOK) {
  const labels = rows.map(row => row.bucket.slice(5));
  const missingPeriods = { id: "financeMissingPeriods", beforeDatasetsDraw(chart) {
    if (!chart.chartArea || rows.length < 2) return;
    const { ctx, chartArea: area, scales: { x } } = chart;
    const step = Math.abs(x.getPixelForValue(1) - x.getPixelForValue(0));
    let start = -1;
    for (let i = 0; i <= rows.length; i++) {
      const missing = i < rows.length && !FinanceCore.hasLedger(rows[i]);
      if (missing && start === -1) start = i;
      if (!missing && start !== -1) {
        const left = Math.max(area.left, x.getPixelForValue(start) - step / 2), right = Math.min(area.right, x.getPixelForValue(i - 1) + step / 2);
        ctx.save(); ctx.fillStyle = "#f3f5f0"; ctx.fillRect(left, area.top, right - left, area.bottom - area.top);
        if (right - left > 85) { ctx.fillStyle = "#9aa78e"; ctx.font = "10px sans-serif"; ctx.textAlign = "center"; ctx.fillText("账单待补齐", (left + right) / 2, area.top + 18); }
        ctx.restore(); start = -1;
      }
    }
  } };
  financeChart("financeCash", "finance-cash-chart", { type: "bar", plugins: [missingPeriods], data: { labels, datasets: [
    { type: "line", label: "结算回款净额", data: rows.map(row => currencyOK && row.settled_rows ? row.payback_amount : null), borderColor: "#568871", backgroundColor: "#56887112", tension: .22, borderWidth: 2, pointRadius: rows.length > 40 ? 0 : 3, pointBackgroundColor: "#fff", spanGaps: false },
    { type: "bar", label: "其他费用收支", data: rows.map(row => currencyOK && row.fee_rows ? row.known_fee_balance_amount : null), backgroundColor: "#cc9a7880", borderRadius: 3, maxBarThickness: 19 },
    { type: "line", label: "平台收支净额", data: rows.map(row => currencyOK && FinanceCore.hasLedger(row) ? row.platform_balance_amount : null), borderColor: "#7892b3", backgroundColor: "#7892b312", tension: .22, borderWidth: 2, pointRadius: rows.length > 40 ? 0 : 3, pointBackgroundColor: "#fff", spanGaps: false },
  ] }, options: financeChartOptions() });
  document.getElementById("finance-cash-empty").hidden = currencyOK && FinanceCore.hasLedger(totals);
  document.getElementById("finance-cash-empty").textContent = currencyOK ? "所选期间没有已导入的结算 / 费用账单" : "账单币种待核对，暂停金额合计";
  const expenses = [
    ["发货面单", Math.max(0, -totals.shipping_label_fee_amount)],
    ["销售 / 运费冲回", Math.max(0, -totals.sales_chargeback_amount) + Math.max(0, -totals.freight_chargeback_amount)],
    ["退货面单", Math.max(0, -totals.return_label_fee_merchant_amount) + Math.max(0, -totals.return_label_fee_third_party_amount)],
    ["履约违规", Math.abs(totals.fulfillment_delay_amount) + Math.abs(totals.fulfillment_false_ship_amount)],
    ["拒付 / 处置", Math.abs(totals.buyer_chargeback_amount) + Math.abs(totals.disposal_fee_amount)],
  ].filter(([, value]) => value > 0 && currencyOK);
  const totalExpense = expenses.reduce((sum, [, amount]) => sum + amount, 0);
  financeChart("financeExpenses", "finance-expense-chart", {
    type: "doughnut",
    data: {
      labels: expenses.map(x => x[0]),
      datasets: [{ data: expenses.map(x => x[1]), backgroundColor: financeColors, borderColor: "#fff", borderWidth: 3, hoverOffset: 4 }],
    },
    options: {
      responsive: true, maintainAspectRatio: false, animation: false, cutout: "74%",
      plugins: { legend: { display: false }, tooltip: { callbacks: { label: item => `${item.label}: ${financeMoney(item.raw)}` } } },
    },
  });
  document.getElementById("finance-expense-empty").hidden = expenses.length > 0;
  document.getElementById("finance-expense-legend").innerHTML = expenses.map(([label, amount], i) => `<div><i style="background:${financeColors[i]}"></i><span>${label}</span><strong>${financeMoney(amount)}</strong><small>${(amount / totalExpense * 100).toFixed(1)}%</small></div>`).join("") || '<div>暂无已导入费用</div>';
  financeChart("financeCoverage", "finance-coverage-chart", { type: "bar", data: { labels, datasets: [
    { label: "销量", data: rows.map(row => row.units), backgroundColor: "#99b7a580", borderRadius: 3, maxBarThickness: 22, yAxisID: "y" },
    { type: "line", label: "价格覆盖率", data: rows.map(FinanceCore.coverage), borderColor: "#c49a6d", borderWidth: 2, pointRadius: 2, tension: .2, yAxisID: "coverage" },
  ] }, options: financeChartOptions({ scales: { x: { grid: { display: false }, ticks: { color: "#8b9b88", maxTicksLimit: 8, maxRotation: 0, font: { size: 10 } } }, y: { beginAtZero: true, border: { display: false }, grid: { color: "#edf1e8" }, ticks: { color: "#8b9b88", font: { size: 10 } } }, coverage: { min: 0, max: 100, position: "right", border: { display: false }, grid: { display: false }, ticks: { color: "#b09977", stepSize: 25, callback: value => value + "%", font: { size: 10 } } } }, plugins: { legend: { position: "bottom", labels: { boxWidth: 7, usePointStyle: true, color: "#7c8b80", font: { size: 10 } } }, tooltip: { callbacks: { label: item => `${item.dataset.label}: ${item.dataset.yAxisID === "coverage" ? financePercent(item.raw) : formatNumber(item.raw)}` } } } }) });
}

function renderFinanceQuality(status) {
  const sources = status?.sources || [], coverage = status?.coverage || [], pending = status?.pending || [];
  const imports = sources.map(x => x.imported_at).filter(Boolean).sort();
  const latest = imports.at(-1), age = latest ? Math.max(0, Math.round((new Date(financeDate(status.generated_at)) - new Date(financeDate(latest))) / 86400000)) : null;
  const exact = financeN(coverage, "exact_lines"), inferred = financeN(coverage, "inferred_lines"), firstSeen = financeN(coverage, "first_seen_lines");
  const overlap = financeN(pending, "overlap_orders");
  const row = (title, detail, badge, kind) => `<div class="finance-quality-row"><div><strong>${title}</strong><small>${escapeHtml(detail)}</small></div>${financePill(badge, kind)}</div>`;
  document.getElementById("finance-quality").innerHTML = [
    row("账单更新", latest ? `最近导入 ${financeDate(latest)}` : "所选店铺尚未导入财务账单", age == null ? "未导入" : age > 7 ? `已过 ${age} 天` : "已更新", age == null || age > 7 ? "warning" : ""),
    row("价格匹配质量", `确认区间 ${formatNumber(exact)} 行 · 时间外推 ${formatNumber(inferred)} 行`, inferred ? "含推断" : exact ? "有确认匹配" : "待回填", inferred || !exact ? "warning" : ""),
    row("已结算重叠", `${formatNumber(overlap)} 个待处理 PO 同时有已到账记录`, overlap ? "待核对" : "无重叠", overlap ? "warning" : ""),
    row("订单时间口径", firstSeen ? `${formatNumber(firstSeen)} 行使用首次抓取时间` : "暂无使用首次抓取时间的订单行", firstSeen ? "非下单时间" : "已检查", firstSeen ? "muted" : ""),
    row("订单 / 库存同步", status?.sync?.completed_at ? `最近完成 ${financeDate(status.sync.completed_at)} · 订单阶段可能单独更新` : "暂无同步记录", status?.sync?.status === "failed" ? "本轮失败" : status?.sync?.status === "succeeded" ? "成功" : "待同步", status?.sync?.status === "failed" ? "error" : "muted"),
  ].join("");
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
  const report = state.profitSKU, rows = financeSKURows(), all = report.data?.rows || [], totals = FinanceCore.sumRows(all);
  const currencyOK = financeCurrencyOK(report.status), coverage = FinanceCore.coverage(totals);
  const hasSales = all.some(row => row.settled_rows > 0);
  document.getElementById("finance-sku-metrics").innerHTML = [
    financeMetric("已到账销售回款", financeMoney(hasSales && currencyOK ? totals.sales_receipt_amount : null), "全区间合计 · 未减冲回", "circle-dollar-sign", "featured"),
    financeMetric("活跃 SKU", formatNumber(all.length), "所选区间有订单或回款", "package"),
    financeMetric("商品销量", formatNumber(totals.units), "按系统订单日期统计", "shopping-bag"),
    financeMetric("估算销售额", financeMoney(currencyOK && totals.estimated_sales_matched ? totals.estimated_sales_amount : null), "仅已定价订单行", "scan-line", "warning"),
    financeMetric("毛利润 / 毛利率", "待接入", "费用归因与商品成本待完善", "calculator", "unavailable"),
  ].join("");
  const latestBill = (report.status?.sources || []).filter(x => x.source === "settled").map(x => x.last_at).filter(Boolean).sort().at(-1);
  financeNotice("finance-sku-notice", "这里展示 SKU 销售回款，暂不能按 SKU 计算利润", `${latestBill ? `已导入结算截至 ${financeDate(latestBill)}，之后的订单回款数据待补齐。` : "所选店铺尚未导入结算账单。"} 运费、面单、违规等订单级费用尚未完整归因。当前价格覆盖 ${financePercent(coverage)}；${currencyOK ? "估算不外推至未定价行。" : "账单包含非 USD / 未知币种，金额合计已暂停展示。"}`, "info");
  setText("profit-sku-range", `${report.data?.range.start || "—"} 至 ${report.data?.range.end || "—"} · 当前显示 ${rows.length} / ${all.length} 个 SKU`);
  document.getElementById("profit-sku-body").innerHTML = rows.map(row => `<tr><td><span class="finance-sku-code">${escapeHtml(row.platform_sku)}</span><span class="finance-product" title="${escapeHtml(row.sku_name)}">${escapeHtml(row.sku_name || "暂无商品名称")}</span></td><td class="num">${formatNumber(row.units)}</td>${financeAmountCell(row.estimated_sales_amount, currencyOK && row.estimated_sales_matched > 0)}${financeCoverageCell(row)}${financeAmountCell(row.sales_receipt_amount, currencyOK && row.settled_rows > 0)}${financeAmountCell(row.sales_chargeback_amount, currencyOK && row.settled_rows > 0)}${financeAmountCell(row.payback_amount, currencyOK && row.settled_rows > 0)}<td class="num">${formatNumber(row.refund_orders)}</td></tr>`).join("") || emptyRow(8, report.loaded ? "没有匹配的 SKU，请调整搜索或日期" : "报表数据暂不可用");
  const top = all.filter(row => row.sales_receipt_amount > 0 && currencyOK).slice().sort((a, b) => b.sales_receipt_amount - a.sales_receipt_amount).slice(0, 10);
  financeChart("financeSKU", "finance-sku-chart", { type: "bar", data: { labels: top.map(row => row.platform_sku), datasets: [{ label: "销售回款", data: top.map(row => row.sales_receipt_amount), backgroundColor: "#80a48b", borderRadius: 3, maxBarThickness: 15 }] }, options: financeChartOptions({ indexAxis: "y", plugins: { legend: { display: false }, tooltip: { callbacks: { title: items => top[items[0]?.dataIndex]?.platform_sku || "", label: item => financeMoney(item.raw) } } }, scales: { x: { grid: { color: "#edf1e8" }, border: { display: false }, ticks: { color: "#91a08f", font: { size: 10 } } }, y: { grid: { display: false }, border: { display: false }, ticks: { color: "#78906f", font: { size: 9 }, callback: function(value) { const label = this.getLabelForValue(value); return label.length > 28 ? label.slice(0, 26) + "…" : label; } } } } }) });
  document.getElementById("finance-sku-empty").hidden = top.length > 0;
  document.getElementById("finance-sku-coverage").innerHTML = `<strong>${financePercent(coverage)}</strong><p>${formatNumber(totals.estimated_sales_matched)} 个已定价订单行 / ${formatNumber(totals.estimated_sales_total)} 个总订单行</p><div class="finance-progress"><i style="width:${Math.min(100, coverage || 0)}%"></i></div><small>未匹配订单暂不计入估算销售额。活动价反推的金额可能与实际成交回款不同。</small>`;
  lucide.createIcons();
}

async function loadProfitUnsettledSummary() {
  const params = new URLSearchParams({ shop_key: state.profitUnsettled.shopKey });
  await financeLoad("profitUnsettled", "profit-unsettled-refresh", "finance-pending-notice", "finance-pending-export", [`/api/profit/unsettled-summary?${params}`, `/api/profit/report-status?${params}`], renderProfitUnsettledSummary);
}

function renderProfitUnsettledSummary() {
  const report = state.profitUnsettled, shops = report.data?.shops || [], checks = report.status?.pending || [], currencyOK = financeCurrencyOK(report.status);
  const overlaps = financeN(checks, "overlap_orders"), overlapAmount = financeN(checks, "overlap_net_amount"), net = financeN(checks, "net_amount"), orderCount = financeN(checks, "orders"), labelRows = financeN(checks, "label_rows"), discountedRows = financeN(checks, "discounted_rows");
  document.getElementById("finance-pending-metrics").innerHTML = [
    financeMetric("快照净待回款", financeMoney(orderCount && currencyOK && !discountedRows ? net : null), discountedRows ? "含非零折后列，回款口径待确认" : "原始待处理金额 · 未自动核销", "hourglass", "featured"),
    financeMetric("待处理 PO", formatNumber(orderCount), "待处理表中的订单", "receipt-text"),
    financeMetric("待出账面单费", financeMoney(labelRows && currencyOK ? Math.abs(financeN(checks, "label_amount")) : null), `${formatNumber(labelRows)} 行 · 未与待回款订单对齐`, "truck"),
    financeMetric("重叠待回款", financeMoney(overlaps && currencyOK && !discountedRows ? overlapAmount : null), `${formatNumber(overlaps)} 个 PO 同时有已到账记录`, "files", "warning"),
    financeMetric("净利润", "待核对", "订单未对齐，成本未接入", "calculator", "unavailable"),
  ].join("");
  const imported = (report.status?.sources || []).filter(x => x.source === "unsettled").map(x => x.imported_at).filter(Boolean).sort().at(-1);
  financeNotice("finance-pending-notice", overlaps ? `${formatNumber(overlaps)} 个待处理 PO 已有到账记录，请核对后回补` : "待处理金额来自导入快照，需持续核对结算状态", `快照导入日期 ${financeDate(imported)}。${overlaps ? `重叠记录尚未自动核销，不能直接与已到账金额相加。` : "这里展示的不是实时平台余额。"} 待出账面单与待回款订单未对齐，不能直接相减得出利润。${discountedRows ? "存在非零折后回款列，金额含义待确认，净额已暂停展示。" : ""}${currencyOK ? "" : "含非 USD / 未知币种，金额合计已暂停展示。"}`);
  document.getElementById("profit-unsettled-shop-body").innerHTML = shops.map(shop => {
    const check = checks.find(x => x.shop_key === shop.shop_key) || {};
    return `<tr><td>${escapeHtml(financeShopName(shop.shop_key))}</td>${financeAmountCell(shop.sales_receipt, currencyOK)}${financeAmountCell(-Math.abs(shop.sales_chargeback), currencyOK)}${financeAmountCell(shop.freight_receipt, currencyOK)}${financeAmountCell(-Math.abs(shop.freight_chargeback), currencyOK)}${financeAmountCell(Number(shop.sales_receipt_after_discount || 0) + Number(shop.freight_receipt_after_discount || 0), currencyOK, "含义待确认")}${financeAmountCell(shop.pending_shipping_label_fee, currencyOK)}<td class="num">${formatNumber(check.overlap_orders)}</td>${financeAmountCell(check.overlap_net_amount, currencyOK)}</tr>`;
  }).join("") || emptyRow(9, "所选店铺没有已导入的待结算数据");
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
