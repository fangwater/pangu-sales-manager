const systemGuideState = { model: "profit-summary", shop: "", selected: "calculate", controller: null, status: null };
const reportModels = [
  { id: "profit-summary", name: "财务总览", icon: "wallet", sources: [["已到账结算", "销售、运费与冲回", "到账时间 · 币种 · 金额", "settled"], ["平台费用账单", "面单、违规、拒付与处置", "记账时间 · 费用类型 · 金额", "fees"], ["订单与历史价格", "商品数量、单价与取消记录", "订单行 · SKU · 数量 · 匹配价格", "orders"]], group: ["按期间汇总", "店铺 → 日 / 周 / 月", "同类金额分别求和"], calculate: ["结算 + 费用", "回款减冲回，再计费用", "估算销售额单独展示"], output: ["收支报表", "趋势 · 瀑布图 · 明细", "未扣采购、头程等内部成本"], note: "没有账单的期间保留空白；订单销量、到账与费用分别按各自业务时间汇总。" },
  { id: "profit-sku", name: "商品回款", icon: "package", sources: [["标准订单行", "SKU、数量与取消记录", "商品标识 · 数量 · 订单时间", "orders"], ["带 SKU 的结算", "销售回款与销售冲回", "店铺 · SKU · 到账金额", "settled"], ["订单匹配价格", "历史价格与订单行关联", "匹配单价 × 数量", "prices"]], group: ["关联商品", "店铺 + 平台 SKU", "订单与结算各自汇总"], calculate: ["回款 − 冲回", "得到已归因结算净额", "已定价行计算销售额"], output: ["商品回款报表", "排行榜 · 分布 · SKU 明细", "订单级费用尚未完整分摊"], note: "只归因带商品标识的结算；订单级运费和费用不强行分摊到商品。" },
  { id: "profit-unsettled", name: "待结算", icon: "hourglass", sources: [["待处理回款快照", "销售、运费及两类冲回", "店铺 · PO · 币种 · 金额", "unsettled"], ["待出账面单", "面单费与待出账记录", "面单标识 · 费用 · 店铺", "shipping_pending"], ["已到账记录", "用于核对重复出现的 PO", "店铺 · PO · 到账记录", "settled"]], group: ["保留导入快照", "按店铺、币种分别汇总", "SKU 申报额单独汇总"], calculate: ["回款 − 冲回", "关联已到账 PO 检查重叠", "保留原额，不自动核销"], output: ["待结算报表", "金额构成 · 重叠 PO · 明细", "面单费与回款不直接相减"], note: "出现非零折后回款时暂停净额合计，等待口径确认。重叠 PO 需逐笔核对部分结算。" },
  { id: "overview", name: "销售总览", icon: "chart-no-axes-combined", sources: [["TEMU / SHEIN 订单", "同步后的标准订单行", "平台 · 店铺 · 状态 · 数量", "orders"], ["SKU 映射", "平台商品与仓库商品配对", "平台 SKU · 仓库 SKU · 换算系数", "mappings"], ["XLWMS 库存", "最近一次成功库存快照", "仓库 · SKU · 可用库存", "inventory"]], group: ["筛选与归一", "平台 / 店铺 / 仓库", "数量按映射系数换算"], calculate: ["按日 / 周 / 月求和", "销量、订单数与同期变化", "关联当前库存"], output: ["销售总览", "销量趋势 · 平台分布", "TEMU 以首次采集时间记日"], note: "库存来自最近一次成功快照；销量以系统已保存的有效订单行为基础。" },
  { id: "skus", name: "SKU 分析", icon: "boxes", sources: [["标准订单行", "当前及对比期间销量", "时间 · 仓库 SKU · 数量", "orders"], ["SKU 映射", "平台数量换算为仓库数量", "商品配对 · 换算系数", "mappings"], ["仓库库存", "商品可用库存", "仓库 SKU · 可用数量", "inventory"]], group: ["按仓库 SKU 聚合", "跨店铺与平台归并", "对比上一期间销量"], calculate: ["销量 + 库存分析", "增长、可售天数与需求预测", "零销量时不推算可售天数"], output: ["SKU 分析表", "商品销量与库存明细", "预测列展示数量"], note: "库存快照与订单时间独立；需求预测按系统已保存的订单计算。" },
  { id: "warehouses", name: "仓库库存", icon: "warehouse", sources: [["XLWMS 库存", "各仓库保存的可用数量", "仓库编码 · SKU · 可用库存", "inventory"], ["归属仓库订单", "映射后的订单销量", "仓库编码 · 销量", "orders"], ["仓库目录", "仓库编码与名称", "仓库编码 · 展示名称", "warehouses"]], group: ["按仓库归并", "库存与订单分别汇总", "保留各仓库边界"], calculate: ["汇总库存与销量", "可用库存、销量、活跃商品", "无归属订单不强行分摊"], output: ["仓库报表", "仓库对比图 · 数量明细", "库存同步失败保留旧快照"], note: "仓库库存与本期销量使用不同时间口径，分别展示。" },
  { id: "mappings", name: "SKU 映射", icon: "git-merge", sources: [["平台商品", "TEMU / SHEIN 商品标识", "平台 · 店铺 · SKU", "orders"], ["仓库商品", "XLWMS 商品标识", "仓库 SKU", "inventory"], ["配对记录", "自动匹配与手动修正", "配对来源 · 换算系数", "mappings"]], group: ["以平台商品定位", "平台 + 店铺 + SKU", "每个商品保存当前配对"], calculate: ["设置仓库 SKU", "平台数量 × 换算系数", "为销量与库存提供连接"], output: ["映射明细", "商品配对 · 系数 · 状态", "编辑后重新读取报表"], note: "映射系数改变数量换算；未映射商品不能直接归入仓库 SKU。" },
  { id: "orders", name: "标准订单", icon: "list-ordered", sources: [["TEMU 订单", "采集到的订单与商品行", "店铺 · PO · SKU · 数量", "orders"], ["SHEIN 订单", "系统接入的订单记录", "店铺 · 订单号 · 状态", "orders"], ["SKU 配对与仓库", "商品换算与销售归属", "仓库 SKU · 换算系数", "mappings"]], group: ["统一订单结构", "平台 + 店铺 + 订单号", "保留商品行与时间来源"], calculate: ["状态与数量归一", "换算仓库商品数量", "标记订单日期的来源"], output: ["标准订单表", "平台 · 订单 · 仓库 · 商品行", "按页读取系统订单"], note: "TEMU 当前订单日期使用首次采集时间；日期来源在订单表中直接展示。" },
  { id: "activity-prices", name: "活动价格", icon: "tags", sources: [["活动报名记录", "商品、活动类型与报名状态", "报名 ID · SKC · SKU", "activity"], ["站点活动价格", "活动与日常价格", "站点 · 币种 · 价格", "activity"], ["商品与库存证据", "当前商品清单与活动库存", "剩余库存 · 当前商品记录", "activity"]], group: ["关联活动商品", "报名 + SKC + SKU + 站点", "保留最近一次采集快照"], calculate: ["判断当前生效活动", "结合状态、库存及商品证据", "价格按分换算展示"], output: ["活动价格快照", "报名 · 商品 · 活动价 · 库存", "展示当前活动采集结果"], note: "快照表示最近采集时的状态，不能直接作为任意历史订单的成交价。" },
  { id: "sku-prices", name: "SKU 价格", icon: "badge-dollar-sign", sources: [["日常商品价", "SKU 的日常价格", "SKU · 币种 · 日常价", "prices"], ["生效活动价", "当前有效活动的报价", "报名 ID · SKU · 活动价", "activity"], ["价格历史区间", "价格解析与更新时间", "起始时间 · 更新时间 · 来源", "prices"]], group: ["按 SKU 解析", "关联当前活动与日常报价", "保留价格变更的时间区间"], calculate: ["选择当前价格", "活动价或日常价", "用于订单行价格回填"], output: ["SKU 价格表", "当前价格 · 币种 · 来源", "原始分值除以 100 展示"], note: "价格表展示当前解析结果，财务估算使用已经回填到订单行的价格。" },
  { id: "profit", name: "账单导入", icon: "file-input", sources: [["结算与费用文件", "按账单类型识别记录", "店铺 · 文件类型 · 币种", "files"], ["导入规则", "解析金额、日期与记录标识", "业务字段 · 唯一标识", "rules"], ["导入任务", "文件数、写入行与执行状态", "任务 ID · 执行时间", "imports"]], group: ["按数据集写入", "店铺与业务记录标识去重", "待处理与已到账分别保存"], calculate: ["更新对应数据集", "新增或更新业务记录", "记录导入结果"], output: ["导入统计", "各数据集行数与最近任务", "财务报表读取已保存账单"], note: "账单上传与订单库存同步独立；重复文件按业务标识更新记录。" },
];
const guideModel = () => reportModels.find(model => model.id === systemGuideState.model);
const guideNumber = value => value == null ? "—" : formatNumber(value);
const guidePrice = (value, currency) => {
  if (value == null) return "—";
  if (!currency) return `${(Number(value) / 100).toFixed(2)}（币种未提供）`;
  try { return new Intl.NumberFormat("en-US", { style: "currency", currency: currency || "USD" }).format(Number(value) / 100); }
  catch (_) { return `${(Number(value) / 100).toFixed(2)} ${currency || ""}`; }
};
function guideTime(value) {
  if (!value || !Number.isFinite(new Date(value).getTime())) return "暂无记录";
  return new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(value));
}
function bindSystemGuide() {
  document.getElementById("system-guide-content").innerHTML = `
    <div class="report-workbench">
      <div class="report-controls"><div class="report-model-tabs" role="group" aria-label="财务报表构造">${reportModels.slice(0, 3).map(model => `<button type="button" data-guide-model="${model.id}"><i data-lucide="${model.icon}"></i>${model.name}</button>`).join("")}</div><label class="report-select"><span>所有报表</span><select id="guide-model-select">${reportModels.map(model => `<option value="${model.id}">${model.name}</option>`).join("")}</select></label><label class="report-select" id="guide-shop-control"><span>店铺</span><select id="guide-shop"><option value="">全部 TEMU 店铺</option><option value="panda-homes">Panda Homes</option><option value="panda-buy">Panda Buy</option></select></label><button id="guide-print" type="button" class="secondary-command"><i data-lucide="printer"></i>打印</button></div>
      <article class="panel report-flow-panel"><div class="panel-heading"><div><h2 id="guide-model-title"></h2><span>数据输入 → 关联汇总 → 计算 → 报表</span></div><button class="text-command" id="guide-open-report" type="button">打开报表 <i data-lucide="arrow-up-right"></i></button></div><div id="guide-flow-diagram" class="model-flow"></div><div id="guide-node-detail" class="flow-node-detail" aria-live="polite"></div><div id="guide-model-note" class="report-scope"></div></article>
      <article class="panel report-preview-panel"><div class="panel-heading"><div><h2>实际报表预览</h2><span id="guide-preview-range">读取已保存数据</span></div><button id="guide-refresh" type="button" class="secondary-command"><i data-lucide="refresh-cw"></i>刷新</button></div><div id="guide-preview" aria-live="polite"></div></article>
      <section id="guide-sync" class="report-update-section"><div class="report-update-heading"><h3>数据更新状态</h3><span id="guide-status-updated"></span></div><div id="guide-live-status" class="report-update-grid"></div></section>
    </div>`;
  const root = document.getElementById("system-guide-content");
  root.addEventListener("click", event => {
    const tab = event.target.closest("[data-guide-model]"), node = event.target.closest("[data-guide-node]");
    if (tab) selectGuideModel(tab.dataset.guideModel);
    if (node) selectGuideNode(node.dataset.guideNode);
  });
  root.addEventListener("keydown", event => { const node = event.target.closest("[data-guide-node]"); if (node && ["Enter", " "].includes(event.key)) { event.preventDefault(); selectGuideNode(node.dataset.guideNode); } });
  document.getElementById("guide-model-select").addEventListener("change", event => selectGuideModel(event.target.value));
  document.getElementById("guide-shop").addEventListener("change", event => { systemGuideState.shop = event.target.value; loadSystemGuideStatus(); });
  document.getElementById("guide-refresh").addEventListener("click", loadSystemGuideStatus);
  document.getElementById("guide-open-report").addEventListener("click", () => switchView(systemGuideState.model));
  document.getElementById("guide-print").addEventListener("click", () => window.print());
  const source = document.getElementById("source-status");
  source.setAttribute("role", "button"); source.setAttribute("tabindex", "0"); source.setAttribute("aria-label", "查看数据更新状态");
  const openStatus = async () => { await switchView("system-guide"); const url = new URL(location.href); url.hash = "guide-sync"; history.replaceState(null, "", url); scrollToSystemGuideSection(url.hash); };
  source.addEventListener("click", openStatus);
  source.addEventListener("keydown", event => { if (["Enter", " "].includes(event.key)) { event.preventDefault(); openStatus(); } });
  window.addEventListener("hashchange", () => { if (state.view === "system-guide") scrollToSystemGuideSection(location.hash); });
  renderGuideModel();
}
function selectGuideModel(id) {
  systemGuideState.model = id; systemGuideState.selected = "calculate";
  renderGuideModel(); loadSystemGuideStatus();
}
function renderGuideModel() {
  const model = guideModel();
  document.getElementById("guide-model-select").value = model.id;
  document.querySelectorAll("[data-guide-model]").forEach(button => { const active = button.dataset.guideModel === model.id; button.classList.toggle("active", active); button.setAttribute("aria-pressed", String(active)); });
  document.getElementById("guide-shop-control").hidden = !model.id.startsWith("profit-");
  setText("guide-model-title", `${model.name} · 构造示意图`);
  setText("guide-model-note", model.note);
  const nodes = model.sources.map((source, i) => ({ id: `source-${i}`, title: source[0], lines: [source[1]], tag: "数据来源" })).concat([
    { id: "group", title: model.group[0], lines: model.group.slice(1), tag: "01 / 汇总" },
    { id: "calculate", title: model.calculate[0], lines: model.calculate.slice(1), tag: "02 / 计算" },
    { id: "output", title: model.output[0], lines: model.output.slice(1), tag: "03 / 输出" },
  ]);
  const nodeSVG = (node, x, y, w, h, mobile) => `<g class="flow-node ${node.id === "output" ? "output" : ""}" data-guide-node="${node.id}" role="button" tabindex="0" aria-label="${escapeHtml(node.title)}"><rect x="${x}" y="${y}" width="${w}" height="${h}" rx="13"/><text class="flow-tag" x="${x + 18}" y="${y + 23}">${node.tag}</text><text class="flow-title" x="${x + 18}" y="${y + 47}">${escapeHtml(node.title)}</text>${node.lines.map((line, i) => `<text class="flow-description" x="${x + 18}" y="${y + 69 + i * 21}">${escapeHtml(line)}</text>`).join("")}</g>`;
  const markers = suffix => `<defs><marker id="flow-arrow-${suffix}" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8" fill="#c7d2fe"/></marker></defs>`;
  const desktopPaths = ["M250 68 H278 V167 H310", "M250 167 H310", "M250 266 H278 V167 H310", "M550 167 H594", "M834 167 H878"];
  document.getElementById("guide-flow-diagram").innerHTML = `<svg class="flow-desktop" viewBox="0 0 1140 335" role="group" aria-label="${escapeHtml(model.name)}数据流图">${markers("d")}${desktopPaths.map(d => `<path class="flow-connector" d="${d}" marker-end="url(#flow-arrow-d)"/>`).join("")}${nodes.map((node, i) => i < 3 ? nodeSVG(node, 10, 21 + i * 99, 240, 94) : nodeSVG(node, [310, 594, 878][i - 3], 107, 240, 120)).join("")}</svg>
    <svg class="flow-mobile" viewBox="0 0 350 707" role="group" aria-label="${escapeHtml(model.name)}数据流图">${markers("m")}${["M310 57 H330 V325 H175 V341", "M310 161 H330", "M310 265 H330", "M175 461 V485", "M175 605 V629"].map(d => `<path class="flow-connector" d="${d}" marker-end="url(#flow-arrow-m)"/>`).join("")}${nodes.map((node, i) => nodeSVG(node, 10, [10, 114, 218, 341, 485, 629][i], i < 3 ? 300 : 330, i < 3 ? 94 : 120)).join("")}</svg>`;
  // The portrait diagram keeps full text visible while fitting a phone screen.
  document.querySelector(".flow-mobile").setAttribute("viewBox", "0 0 350 759");
  selectGuideNode(systemGuideState.selected);
  lucide.createIcons();
}
function selectGuideNode(id) {
  systemGuideState.selected = id;
  document.querySelectorAll("[data-guide-node]").forEach(node => { const active = node.dataset.guideNode === id; node.classList.toggle("selected", active); node.setAttribute("aria-pressed", String(active)); });
  const model = guideModel(), sourceIndex = Number(id.split("-")[1]), source = id.startsWith("source-") ? model.sources[sourceIndex] : null;
  let title, fields;
  if (source) { title = source[0]; fields = source[2].split(" · "); }
  else { const item = model[id]; title = item[0]; fields = item.slice(1); }
  let meta = "";
  if (source && systemGuideState.status) {
    const sourceRows = systemGuideState.status.sources.filter(row => source[3] === "fees" ? !["settled", "unsettled", "shipping_pending"].includes(row.source) : row.source === source[3]);
    if (sourceRows.length) meta = `<span class="flow-source-meta">${formatNumber(financeN(sourceRows, "rows"))} 条记录 · 最近导入 ${guideTime(sourceRows.map(row => row.imported_at).filter(Boolean).sort().at(-1))}</span>`;
  }
  document.getElementById("guide-node-detail").innerHTML = `<span class="flow-detail-label"><i data-lucide="mouse-pointer-2"></i>${escapeHtml(title)}</span><div>${fields.map(field => `<span class="field-chip">${escapeHtml(field)}</span>`).join("")}</div>${meta}`;
  lucide.createIcons();
}
function scrollToSystemGuideSection(hash) {
  const section = document.getElementById(String(hash).replace(/^#/, ""));
  if (section && document.getElementById("system-guide-content").contains(section)) section.scrollIntoView({ block: "start" });
}
function guideTable(headers, rows, footer = "") {
  return `<div class="report-preview-table"><table><thead><tr>${headers.map((label, i) => `<th${i ? ' class="num"' : ""}>${escapeHtml(label)}</th>`).join("")}</tr></thead><tbody>${rows.map(row => `<tr>${row.map((value, i) => `<td${i ? ' class="num"' : ""}>${escapeHtml(String(value ?? "—"))}</td>`).join("")}</tr>`).join("") || emptyRow(headers.length, "暂无对应数据")}</tbody></table></div>${footer ? `<div class="report-preview-foot">${escapeHtml(footer)}</div>` : ""}`;
}
function guideChartShell(title, table, extra = "") {
  return `<div class="report-preview-grid"><div class="report-preview-visual"><h3>${escapeHtml(title)}</h3><div class="report-preview-chart"><canvas id="guide-report-chart" role="img" aria-label="${escapeHtml(title)}"></canvas></div>${extra}</div><div>${table}</div></div>`;
}
async function loadSystemGuideStatus() {
  systemGuideState.controller?.abort();
  const controller = new AbortController(); systemGuideState.controller = controller;
  const model = guideModel(), shop = systemGuideState.shop, signal = controller.signal;
  const button = document.getElementById("guide-refresh"), preview = document.getElementById("guide-preview");
  button.disabled = true; preview.setAttribute("aria-busy", "true");
  destroyChart("financeGuide"); preview.innerHTML = '<div class="report-preview-message">正在读取实际报表…</div>';
  systemGuideState.status = null; selectGuideNode(systemGuideState.selected); document.getElementById("guide-live-status").innerHTML = '<div class="report-update-empty">正在读取更新状态…</div>'; setText("guide-status-updated", "");
  try {
    const params = new URLSearchParams({ shop_key: shop, period: "day" });
    const statusPromise = api(`/api/profit/report-status?${params}`, { signal }).then(response => { if (systemGuideState.controller === controller) { systemGuideState.status = response.data; renderGuideStatus(response.data); selectGuideNode(systemGuideState.selected); } return response.data; });
    let data, status;
    if (model.id.startsWith("profit-")) {
      const path = model.id === "profit-unsettled" ? `/api/profit/unsettled-summary?${params}` : `/api/profit/daily-summary?${params}`;
      [data, status] = await Promise.all([api(path, { signal }).then(response => response.data), statusPromise]);
      if (model.id === "profit-sku") { const skuParams = new URLSearchParams({ shop_key: shop, start: data.range.start, end: data.range.end }); data = (await api(`/api/profit/sku-summary?${skuParams}`, { signal })).data; }
    } else {
      const paths = { mappings: "/api/mappings?page=1&page_size=6", orders: "/api/orders?page=1&page_size=6", "activity-prices": "/api/marketing/activity-snapshot?page=1&page_size=6", "sku-prices": "/api/marketing/sku-price-snapshot?page=1&page_size=6", profit: "/api/profit/summary" };
      [data, status] = await Promise.all([api(paths[model.id] || "/api/dashboard?period=week", { signal }), statusPromise]);
    }
    if (systemGuideState.controller !== controller) return;
    renderGuidePreview(model.id, data, status);
  } catch (error) {
    if (error.name !== "AbortError" && systemGuideState.controller === controller) { destroyChart("financeGuide"); preview.innerHTML = '<div class="report-preview-message error">实际报表读取失败，请点击刷新重试。</div>'; setText("guide-preview-range", "本次数据未取得"); if (!systemGuideState.status) document.getElementById("guide-live-status").innerHTML = '<div class="report-update-empty">更新状态暂不可用</div>'; }
  } finally {
    if (systemGuideState.controller === controller) { button.disabled = false; preview.removeAttribute("aria-busy"); }
    lucide.createIcons();
  }
}
function renderGuideStatus(data) {
  const sync = data.sync || {}, imported = data.sources.map(row => row.imported_at).filter(Boolean).sort().at(-1), last = data.sources.filter(row => row.source === "settled").map(row => row.last_at).filter(Boolean).sort().at(-1);
  const card = (icon, title, value, detail) => `<article><i data-lucide="${icon}"></i><div><span>${escapeHtml(title)}</span><strong>${escapeHtml(value)}</strong><small>${escapeHtml(detail)}</small></div></article>`;
  const failure = sync.status === "failed";
  document.getElementById("guide-live-status").innerHTML = card("refresh-cw", "订单与库存", failure ? "本轮同步有失败" : ({ succeeded: "最近同步成功", running: "正在同步" }[sync.status] || "暂无同步记录"), failure && sync.error?.includes("warehouse_inventory_pkey") ? "库存存在重复仓库 / SKU，保留上次成功快照" : `订单 ${formatNumber(sync.orders_synced || 0)} · 库存 ${formatNumber(sync.inventory_synced || 0)} 行`)
    + card("file-input", "财务账单", imported ? `最近导入 ${financeDate(imported)}` : "尚未导入", last ? `结算到账截至 ${financeDate(last)} · 上传后更新` : "通过账单上传更新")
    + card("tags", "订单定价", `${formatNumber(financeN(data.coverage || [], "priced_lines"))} 个已定价行`, "活动采集 → 历史价格 → 订单回填");
  setText("guide-status-updated", `读取于 ${guideTime(data.generated_at)}`);
}
function renderGuidePreview(id, response, status) {
  const preview = document.getElementById("guide-preview"), ok = financeCurrencyOK(status);
  const money = (value, available = true) => financeMoney(ok && available ? value : null);
  if (id === "profit-summary") {
    const rows = FinanceCore.groupDaily(response.rows, "month", response.range.start, response.range.end), totals = FinanceCore.sumRows(rows), available = ok && FinanceCore.hasLedger(totals);
    const table = guideTable(["期间", "净结算", "费用收支", "收支净额"], rows.map(row => [row.label, money(row.payback_amount, row.settled_rows > 0), money(row.known_fee_balance_amount, row.fee_rows > 0), money(row.platform_balance_amount, FinanceCore.hasLedger(row))]).concat([["合计", money(totals.payback_amount, totals.settled_rows > 0), money(totals.known_fee_balance_amount, totals.fee_rows > 0), money(totals.platform_balance_amount, available)]]), "账单金额按到账 / 记账日期汇总，缺少账单的月份保留空白。");
    preview.innerHTML = guideChartShell("回款到收支净额", table, `<div class="report-result"><span>收支净额</span><strong>${money(totals.platform_balance_amount, available)}</strong><small>未扣内部成本</small></div>`);
    financeWaterfall("guide-report-chart", "financeGuide", totals, available);
    setText("guide-preview-range", `${response.range.start} — ${response.range.end} · USD · 按月汇总`);
  } else if (id === "profit-sku") {
    const rows = response.rows.slice().sort((a, b) => b.sales_receipt_amount - a.sales_receipt_amount).slice(0, 6);
    preview.innerHTML = guideChartShell("销售回款排行", guideTable(["SKU", "销量", "销售回款", "归因净额"], rows.map(row => [row.platform_sku, guideNumber(row.units), money(row.sales_receipt_amount, row.settled_rows > 0), money(row.payback_amount, row.settled_rows > 0)]), "展示销售回款最高的 6 个商品；净额仅含已归因结算。"));
    guideBar(rows.map(row => row.platform_sku), ok ? rows.map(row => row.settled_rows ? row.sales_receipt_amount : null) : [], "销售回款", true, true);
    setText("guide-preview-range", `${response.range.start} — ${response.range.end} · ${response.rows.length} 个 SKU`);
  } else if (id === "profit-unsettled") {
    const checks = status.pending || [], count = financeN(checks, "orders"), discount = financeN(checks, "discounted_rows"), overlap = financeN(checks, "overlap_orders");
    const labels = ["销售回款", "运费回款", "销售冲回", "运费冲回"], values = [financeN(response.shops, "sales_receipt"), financeN(response.shops, "freight_receipt"), -Math.abs(financeN(response.shops, "sales_chargeback")), -Math.abs(financeN(response.shops, "freight_chargeback"))];
    const table = guideTable(["构成", "快照金额"], labels.map((label, i) => [label, money(values[i], count > 0)]).concat([["净待回款", money(financeN(checks, "net_amount"), count > 0 && !discount)], ["重叠待回款", money(financeN(checks, "overlap_net_amount"), overlap > 0 && !discount)]]), `${formatNumber(count)} 个待处理 PO · ${formatNumber(overlap)} 个与已到账重叠，未自动核销。`);
    preview.innerHTML = guideChartShell("待回款构成", table);
    guideBar(labels, ok && count ? values : [], "快照金额", false, true);
    setText("guide-preview-range", "最近导入的待处理快照 · USD");
  } else {
    const data = response.data; let headers, rows, labels, values, title, foot = "", currency = false;
    if (["overview", "skus", "warehouses"].includes(id)) {
      setText("guide-preview-range", `${data.range.start} — ${data.range.end} · 当前保存数据`);
      if (id === "overview") { const series = data.series.slice(-6); headers = ["期间", "订单", "平台销量", "仓库销量"]; rows = series.map(row => [row.label, guideNumber(row.orders), guideNumber(row.platform_units), guideNumber(row.warehouse_units)]); labels = series.map(row => row.label); values = series.map(row => row.warehouse_units); title = "每周仓库销量"; }
      if (id === "skus") { const items = data.skus.slice(0, 6); headers = ["仓库 SKU", "销量", "库存", "可售天数"]; rows = items.map(row => [row.warehouse_sku, guideNumber(row.warehouse_units), guideNumber(row.available_stock), guideNumber(row.days_of_cover)]); labels = items.map(row => row.warehouse_sku); values = items.map(row => row.warehouse_units); title = "商品销量"; }
      if (id === "warehouses") { headers = ["仓库", "库存", "本期销量", "活跃 SKU"]; rows = data.warehouses.map(row => [row.name || row.code, guideNumber(row.available_stock), guideNumber(row.warehouse_units), guideNumber(row.active_sku_count)]); labels = data.warehouses.map(row => row.code); values = data.warehouses.map(row => row.available_stock); title = "仓库可用库存"; }
    } else {
      setText("guide-preview-range", "当前保存快照 · 前 6 条记录");
      if (id === "mappings") { headers = ["平台 SKU", "仓库 SKU", "换算系数", "来源"]; rows = data.items.map(row => [row.platform_sku, row.warehouse_sku || "未配对", guideNumber(row.conversion_factor), row.mapping_source]); foot = `共 ${formatNumber(data.total)} 条映射`; }
      if (id === "orders") { headers = ["订单号", "店铺", "状态", "商品行"]; rows = data.items.map(row => [row.order_no, financeShopName(row.shop_key), row.normalized_status, guideNumber(row.lines.length)]); foot = `共 ${formatNumber(data.total)} 个标准订单`; }
      if (id === "activity-prices") { headers = ["SKU", "站点", "活动价", "剩余库存"]; rows = data.map(row => [row.sku_id, row.site_name, guidePrice(row.site_activity_price, row.currency), guideNumber(row.remaining_activity_stock)]); foot = "活动价按各行币种展示；当前快照不等于历史订单价格。"; }
      if (id === "sku-prices") { headers = ["SKU", "当前价格", "价格来源", "更新时间"]; rows = data.map(row => [row.sku_id, guidePrice(row.price, row.currency), row.price_source, guideTime(row.update_at)]); foot = "原始价格以分保存，展示时除以 100。"; }
      if (id === "profit") { headers = ["数据集", "保存行数"]; rows = data.tables.map(row => [row.label, guideNumber(row.rows)]); labels = data.tables.map(row => row.label); values = data.tables.map(row => row.rows); title = "账单数据集"; foot = data.latest_import ? `最近任务：${guideTime(data.latest_import.completed_at)} · ${formatNumber(data.latest_import.rows_upserted)} 行写入` : "暂无导入任务"; setText("guide-preview-range", "当前已保存的财务数据集"); }
    }
    const table = guideTable(headers, rows, foot);
    if (labels) { preview.innerHTML = guideChartShell(title, table); guideBar(labels, values, title, true, currency); }
    else preview.innerHTML = table;
  }
  if (!ok && id.startsWith("profit-")) { const message = document.createElement("div"); message.className = "report-preview-foot"; message.textContent = "账单含其他或未知币种，暂停 USD 合计。"; preview.prepend(message); }
}
function guideBar(labels, values, title, horizontal = false, money = false) {
  financeChart("financeGuide", "guide-report-chart", { type: "bar", data: { labels, datasets: [{ label: title, data: values, backgroundColor: values.map((value, i) => value < 0 ? "#fb923c" : financeColors[i % financeColors.length]), borderRadius: 5, maxBarThickness: 28 }] }, options: financeChartOptions({ indexAxis: horizontal ? "y" : "x", plugins: { legend: { display: false }, tooltip: { callbacks: { label: item => money ? financeMoney(item.raw) : guideNumber(item.raw) } } } }) });
}
