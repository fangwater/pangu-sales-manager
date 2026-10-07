const systemGuideState = { controller: null };

const guideSources = [
  ["已出账", "对账中心－账务明细", "结算；拒付；履约违规 1 / 2；平台退货面单承担；处置费", "分工作表保存。结算流水参与回款，其他工作表参与费用收支。", "结算与费用"],
  ["已出账", "结算数据－已到账款项－PO 明细账单", "流水 ID、PO、交易类型、到账金额与时间", "与账务明细的结算流水共用同一数据集，按店铺＋流水 ID 去重。", "结算明细"],
  ["已出账", "结算数据－已到账款项－PO 聚合账单", "PO、商品行、销售 / 运费回款及冲回", "保存用于订单和商品核对；聚合金额不再次叠加进结算总额。", "订单核对"],
  ["已出账", "发货面单费－已出账", "包裹号、运单号、账单类型、费用与记账时间", "按店铺汇总已出账费用；未关联到商品的费用不分配至 SKU。", "已出账费用"],
  ["已出账", "退货面单费－退至商家仓", "资金账单 ID、PO、运单号、金额与时间", "按店铺汇总商家仓退货费用，未归因部分不分配至商品。", "商家仓费用"],
  ["已出账", "退货面单费－退至第三方仓", "资金账单 ID、PO、运单号、金额与时间", "进入第三方仓退货费用，与商家仓分开展示。", "第三方仓费用"],
  ["未出账", "结算数据－待处理款项", "PO、商品数量、申报金额、待回款与冲回", "作为待处理快照；检查与已到账 PO 的重叠，不自动核销。", "待回款"],
  ["未出账", "发货面单费－待出账", "包裹号、运单号、预估面单费", "独立保存预估费用；按包裹、运单和账单信息检查跨状态重叠。", "待面单"],
];

const guideAmounts = [
  ["销售回款", "账务明细 · 结算 / PO 明细", "收入", "已接入", "按交易类型汇总结算流水。"],
  ["运费回款", "账务明细 · 结算 / PO 明细", "收入", "已接入", "运费收入单列，不与销售回款混为同一笔。"],
  ["销售冲回", "结算流水", "扣减", "已接入", "从销售回款中扣除，按结算流水的交易类型识别。"],
  ["运费冲回", "结算流水", "扣减", "已接入", "从运费回款中扣除；与销售冲回分别计数。"],
  ["延迟到货", "账务明细 · 履约违规 1", "支出", "已接入", "按负的绝对值汇总，原始导入金额保留。"],
  ["虚假发货", "账务明细 · 履约违规 2", "支出", "已接入", "按负的绝对值汇总，与冲回属于不同费用。"],
  ["买家拒付", "账务明细 · 支出－买家拒付", "支出", "已接入", "按负的绝对值汇总，笔数不当作退款件数。"],
  ["发货面单费", "发货面单费－已出账", "按账单符号", "已接入", "费用保留账单正负方向；待出账面单不计入此处。"],
  ["商家仓退货面单费", "退货面单费－退至商家仓", "按账单符号", "已接入", "独立分类，避免与第三方仓费用混淆。"],
  ["第三方仓退货面单费", "退货面单费－退至第三方仓", "按账单符号", "已接入", "独立分类，保留资金账单 ID 供核对。"],
  ["平台承担退货面单费", "账务明细 · 其他－退货面单费平台承担", "按账单符号", "已接入", "补偿单独展示并保留实际收支方向，不再次扣作费用。"],
  ["处置费", "账务明细 · 支出－处置费", "支出", "已接入", "按负的绝对值计入平台费用收支。"],
  ["税金代扣", "暂无对应数据源", "待确定", "未接入", "留空，不以零代替。"],
  ["税金退回", "暂无对应数据源", "待确定", "未接入", "留空，需确认来源及收入 / 冲减口径。"],
  ["非订单交易费", "暂无对应数据源", "待确定", "未接入", "留空，不能直接归入某个 SKU。"],
];

function guideBadge(text) {
  const kind = /未|待|部分/.test(text) ? "pending" : "ready";
  return `<span class="guide-badge ${kind}">${escapeHtml(text)}</span>`;
}

function guideArrow(label, pending = false) {
  return `<div class="guide-arrow ${pending ? "pending" : ""}"><svg viewBox="0 0 60 28" role="img" aria-label="${label}"><path d="M2 14H52M43 5L52 14L43 23"/></svg><small>${label}</small></div>`;
}

function guideNode(kicker, title, detail, extra = "") {
  return `<div class="guide-node ${extra}"><small>${kicker}</small><strong>${title}</strong><p>${detail}</p></div>`;
}

const guideReports = [
  { view: "profit-summary", title: "财务总览", open: true,
    source: "TEMU 标准订单、已到账结算流水、已出账面单费及其他平台费用；价格回填结果用于辅助销售额估算。",
    calculation: "按店铺和日期筛选，先汇总每天的订单与账单，再按日 / 周 / 月分桶。销售及运费回款扣除两类冲回得到结算净额，再加有正负方向的平台费用得到平台收支净额。",
    display: "指标卡展示金额与价格覆盖率；趋势图展示回款和费用，环图展示支出构成；期间表可展开各项费用，CSV 导出完整明细列。",
    rule: "订单按标准订单日期，回款按到账时间，费用按记账时间。缺账单的期间显示空白；内部成本未接入，平台收支净额不能作为毛利。" },
  { view: "profit-sku", title: "SKU 财务分析", open: true,
    source: "标准订单商品行，以及带有商品 SKU 外部编码的结算流水。订单价格来自已有的价格回填结果。",
    calculation: "订单按平台 SKU 汇总有效销量与已定价销售额；结算按 SKU 外部编码归组，合并到商品行，计算销售回款、销售冲回和已归因结算净额。分别统计价格匹配行数和取消订单行数。",
    display: "销售回款前 10 名、价格覆盖卡和商品明细表。关键词筛选、排序与 CSV 作用于当前查询结果。",
    rule: "没有对应结算流水的 SKU 显示无数据；运费和订单级费用尚未完整分摊到商品，已归因结算净额不能作为 SKU 利润。销量与结算使用各自业务日期。" },
  { view: "profit-unsettled", title: "待结算检查", open: true,
    source: "待处理款项快照、待出账面单快照，以及已到账结算流水和 PO 聚合数据。",
    calculation: "按店铺和币种汇总销售 / 运费待回款及冲回。按同店铺 PO 查询是否已有到账记录；待出账面单按包裹、运单、账单类型和备注核对已出账记录。商品构成按平台 SKU ID 汇总数量与申报金额。",
    display: "净待回款、待面单费和重叠 PO 指标；店铺核对表、SKU 申报金额构成表及 CSV。使用导入快照，不按日期分桶。",
    rule: "重叠 PO 仅标记供核对，不自动删除或扣除。回款与面单尚未逐订单对齐，不能直接相减为利润；非零折后回款列会暂停净待回款合计。" },
  { view: "overview", title: "销售总览",
    source: "TEMU / SHEIN 标准订单与商品行、仓库 SKU 映射、XLWMS 库存快照。",
    calculation: "筛选平台、店铺和仓库，只统计符合销售资格的订单。订单数去重，商品数量汇总；平台销量经映射换算成仓库销量。按日、周或月分桶，与前一个对应窗口比较增长率。",
    display: "订单、销量、活跃 SKU、库存与映射覆盖指标；销量趋势、平台构成、SKU 排名、仓库销量和补货参考。日视图为近 14 日，周视图为近 12 周，月视图为近 6 个月。",
    rule: "映射覆盖按已确认映射数除以全部映射数计算。销量受平台 / 店铺筛选影响，库存来自仓库快照、按仓库筛选，不表示该店铺独占的库存。" },
  { view: "skus", title: "SKU 分析",
    source: "与销售总览相同的订单商品行及库存快照，按仓库 SKU 汇总。需求预测使用最近 90 日的仓库换算销量序列。",
    calculation: "汇总本期及前期销量、增长率和各仓可用库存。预测日销量基准为近 7 日均值的 65% 加近 28 日均值的 35%，叠加近 28 日趋势并限制极值，累计得到未来 7 / 30 日预测。",
    display: "SKU 的销量、库存、增长率、需求预测与库存覆盖天数；可搜索商品并比较销售和补货需求。",
    rule: "库存覆盖天数为可用库存除以预测日销量基准；基准为零时留空。预测可信度取决于销售历史长度与活跃天数，预测量不代表实际订单或确定的采购数量。" },
  { view: "warehouses", title: "仓库库存",
    source: "XLWMS 最近成功写入的库存快照，以及已经归属到仓库的标准订单商品行。",
    calculation: "可用库存按仓库和仓库 SKU 求和；本期仓库销量按订单行的仓库归属汇总。库存 SKU 数统计可用库存非零的 SKU。",
    display: "每个仓库的可用库存、库存 SKU 数与本期销售，配合仓库对比图查看库存和销售分布。",
    rule: "库存是当前快照，销量是所选期间的统计。库存同步失败时保留上次成功快照；未归属仓库的销售单独保留，不硬分配到已有仓库。" },
  { view: "orders", title: "标准订单",
    source: "TEMU 两个店铺与 SHEIN 的订单及商品数据。来源同步后转为统一订单结构。",
    calculation: "按平台、店铺、来源订单号识别同一订单并更新状态；商品行保存平台数量、仓库 SKU、换算系数及仓库数量，关联到对应订单。",
    display: "分页展示订单号、店铺、业务日期、状态、仓库、商品行数、仓库换算数量和日期来源。",
    rule: "保留日期来源供核对，首次抓取时间不能等同真实下单时间。订单列表展示订单状态，销售报表另按销售资格筛选，二者记录数量可能不同。" },
  { view: "mappings", title: "SKU 映射",
    source: "平台商品 SKU、仓库标准 SKU 及 XLWMS 商品配对数据。",
    calculation: "按平台、店铺和平台 SKU 维护仓库 SKU 配对及换算系数。仓库换算数量由平台商品数量乘以对应系数得到。",
    display: "映射状态、平台 / 店铺 SKU、仓库 SKU、换算系数和商品名称，可按状态或关键词筛选。",
    rule: "TEMU 配对以 XLWMS 配对服务为准，修改需该服务接受后才能确认成功。推断映射与已确认映射分开统计，避免把推断当作已核实结果。" },
  { view: "activity-prices", title: "活动价格",
    source: "TEMU 活动报名、站点价格、活动场次、商品信息及逐次活动库存观察。",
    calculation: "将报名、商品 SKU 与站点 / 场次组合为展示行，用本次与上次剩余库存差识别消耗或增加，再结合活动状态判断生效候选与预警。累计消耗为报名库存减当前剩余库存。",
    display: "活动与报名状态、SKC / SKU、站点和场次、日常价 / 活动价、剩余库存、本次变化与累计消耗，支持筛选和导出。",
    rule: "同一报名的库存可能由多个 SKU 共享，不能将共享库存重复加总。库存消耗用于活动状态判断，不直接当作订单销量或结算收入。" },
  { view: "sku-prices", title: "SKU 价格",
    source: "活动观察解析得到的商品价格状态，以及已保存的 SKU 价格时间区间。",
    calculation: "按 SKU 保存当前价格、币种、价格来源、生效活动与确认 / 预警状态。价格或状态变化时关闭旧区间并开启新区间，相同状态延续当前区间。",
    display: "当前 SKU 价格、来源、状态和生效时间，可按 SKU / SKC 或状态筛选，并导出价格快照。",
    rule: "当前价格快照不能直接套用到历史订单。历史估价按订单时点匹配价格区间，区间外观察价标记为外推；仍需执行订单价格回填后才进入财务估算。" },
  { view: "profit", title: "账单导入",
    source: "指定 TEMU 店铺上传的 XLSX，或包含 XLSX 的 ZIP，支持 8 类结算与费用文件。",
    calculation: "识别文件与工作表类型，解析业务字段并按各自业务键新增或更新；同一导入批次在事务中写入，记录成功 / 失败及新增 / 更新数量。",
    display: "各类数据表的行数、最近导入结果和导入历史，用于检查财务报表的数据来源与更新时间。",
    rule: "重复导入按业务键更新，不把相同结算流水重复累计。已出账与待处理独立保存，导入成功不代表历史价格回填或待结算核销已完成。" },
];

function guideReportCard(report, index) {
  return `<details class="guide-details guide-report" data-guide-report="${report.view}" ${report.open ? "open" : ""}><summary><span class="guide-report-number">${String(index + 1).padStart(2, "0")}</span><strong>${report.title}</strong><i data-lucide="chevron-down"></i></summary><div><div class="guide-report-grid">${[["数据来源", report.source], ["聚合与计算", report.calculation], ["图表与明细", report.display], ["使用口径", report.rule]].map(([label, text]) => `<article><h3>${label}</h3><p>${text}</p></article>`).join("")}</div><div class="guide-links"><button type="button" data-open-view="${report.view}">打开${report.title} <i data-lucide="arrow-up-right"></i></button></div></div></details>`;
}

function renderSystemGuide() {
  document.getElementById("system-guide-content").innerHTML = `
    <div class="guide-hero"><div><span class="guide-kicker">REPORT GUIDE</span><h2>各报表的数据与计算方式</h2><p>从数据来源、聚合计算、图表明细和使用口径四个方面，说明每个页面如何构造。</p></div><button class="secondary-command" id="guide-print" type="button"><i data-lucide="printer"></i>打印说明</button></div>
    <div class="guide-principles"><div><strong>${guideReports.length} 个页面</strong><span>逐项说明来源与构造方式</span></div><div><strong>3 类数据</strong><span>订单库存 · 财务账单 · 活动价格</span></div><div><strong>统一说明口径</strong><span>来源 · 计算 · 展示 · 使用规则</span></div></div>
    <nav class="guide-toc" aria-label="说明目录"><a href="#guide-reports">各报表构造</a><a href="#guide-flow">数据流</a><a href="#guide-sources">账单来源</a><a href="#guide-calculation">金额计算</a><a href="#guide-settlement">待结算核对</a><a href="#guide-sync">更新规则</a></nav>
    <section class="guide-section" id="guide-reports"><div class="guide-section-heading"><span>01</span><div><h2>各报表如何构造</h2><p>展开对应页面，查看数据从输入到展示的处理过程。</p></div></div>${guideReports.map(guideReportCard).join("")}</section>
    <section class="guide-section" id="guide-flow"><div class="guide-section-heading"><span>02</span><div><h2>报表数据流</h2><p>订单与库存、财务账单、活动价格分别更新，再按各自口径形成报表。</p></div></div><figure class="guide-diagram"><div class="guide-flow-head"><span>数据来源</span><span>处理逻辑</span><span>报表输出</span></div>
      <div class="guide-flow-row">${guideNode("订单与库存", "TEMU / SHEIN / XLWMS", "平台订单、仓库库存及 SKU 配对。")}${guideArrow("同步")}${guideNode("标准化", "订单与仓库 SKU", "按平台、店铺、订单去重；通过 SKU 映射和换算系数统一仓库销量。")}${guideArrow("汇总")}${guideNode("销售分析", "销售总览 · SKU · 仓库", "按业务日期和筛选条件聚合销量，与当前库存及映射状态结合。")}</div>
      <div class="guide-flow-row">${guideNode("财务账单", "TEMU 的 8 类 Excel", "按店铺导入结算、费用和待处理文件。")}${guideArrow("导入")}${guideNode("业务键去重", "结算、费用与待处理分开", "结算按店铺＋流水 ID 更新；费用分类保存，待处理作为独立快照。")}${guideArrow("计算")}${guideNode("财务分析", "财务总览 · SKU · 待结算", "汇总结算收支和已归因商品回款，待处理金额单独展示。")}</div>
      <div class="guide-flow-row">${guideNode("活动价格", "活动与 SKU 价格观察", "保存报名、库存变化、价格状态与时间区间。")}${guideArrow("匹配")}${guideNode("订单价格回填", "订单时点 × 商品 SKU", "按时间区间匹配单位价格，区间外观察价标记为外推。")}${guideArrow("估算")}${guideNode("辅助估算", "已定价销售额 · 覆盖率", "只计算已定价有效订单行，不外推至全部销量。")}</div><figcaption>订单、回款和费用分别使用各自业务时间；价格快照与订单价格回填也分别更新。</figcaption></figure></section>
    <section class="guide-section" id="guide-sources"><div class="guide-section-heading"><span>03</span><div><h2>财务报表的账单来源</h2><p>各文件按店铺导入，根据业务字段进入结算、费用或待处理数据集。</p></div></div><div class="guide-filter" role="group" aria-label="按账单阶段筛选"><button class="active" aria-pressed="true" data-guide-filter="全部" type="button">全部 8 类</button><button aria-pressed="false" data-guide-filter="已出账" type="button">已出账</button><button aria-pressed="false" data-guide-filter="未出账" type="button">未出账</button><span id="guide-source-count" aria-live="polite">8 类来源</span></div><div class="guide-table-wrap"><table class="guide-table"><caption class="guide-sr-only">八类 TEMU 账单的数据来源和汇总用途</caption><thead><tr><th>阶段 / 文件</th><th>提供的数据</th><th>处理方式</th><th>汇总用途</th></tr></thead><tbody>${guideSources.map(row => `<tr data-guide-source-state="${row[0]}"><td><small>${row[0]}</small><strong>${row[1]}</strong></td><td>${row[2]}</td><td>${row[3]}</td><td>${guideBadge(row[4])}</td></tr>`).join("")}</tbody></table></div>
      <div class="guide-note"><i data-lucide="layers"></i><div><strong>同一笔结算，只累计一次</strong><p>账务明细的结算表和 PO 明细共用结算流水，按店铺＋流水 ID 合并。PO 聚合账单用于订单与商品核对，聚合金额不再次加入结算总额。各文件覆盖范围可能不同，不能把三份金额直接相加。</p></div></div><details class="guide-details"><summary>订单与费用如何关联 <i data-lucide="chevron-down"></i></summary><div><p>订单和商品信息包括订单编号、产品 ID、店铺 / 站点、MSKU、规格及商品名称；费用信息包括费用名、业务时间、到账时间、包裹号与运单号。</p><p>订单商品行以平台、店铺和订单标识关联；带商品 SKU 外部编码的结算流水可进入 SKU 财务汇总。发货面单主要带包裹 / 运单标识，未完整关联到商品时保留在店铺费用汇总，不直接分配给某个 SKU。</p></div></details></section>
    <section class="guide-section" id="guide-calculation"><div class="guide-section-heading"><span>04</span><div><h2>财务报表的金额计算</h2><p>选择对应口径，查看各指标的计算方式。</p></div></div><div class="guide-tabs" role="tablist" aria-label="金额口径"><button id="guide-tab-posted" role="tab" aria-selected="true" aria-controls="guide-panel-posted" tabindex="0" data-guide-tab="posted" type="button">已到账报表</button><button id="guide-tab-estimate" role="tab" aria-selected="false" aria-controls="guide-panel-estimate" tabindex="-1" data-guide-tab="estimate" type="button">活动价辅助估算</button><button id="guide-tab-pending" role="tab" aria-selected="false" aria-controls="guide-panel-pending" tabindex="-1" data-guide-tab="pending" type="button">待结算快照</button></div>
      <div class="guide-calculation-panel" id="guide-panel-posted" role="tabpanel" aria-labelledby="guide-tab-posted" tabindex="0"><div class="guide-formula"><span>结算回款净额</span><strong>销售回款 ＋ 运费回款 − 销售冲回绝对值 − 运费冲回绝对值</strong></div><div class="guide-formula featured"><span>平台收支净额</span><strong>结算回款净额 ＋ 已导入的平台费用收支</strong><small>支出按负数相加，补偿保留账单方向；内部成本未扣除。</small></div><div class="guide-example"><span>计算示例 · 非真实订单</span><p>销售 100 ＋ 运费 20 − 销售冲回 5 − 运费冲回 1 ＝ 净结算 114；面单 −30、违规 −2、补偿 ＋3，平台收支净额为 <strong>85</strong>。缺少内部成本时不输出毛利。</p></div></div>
      <div class="guide-calculation-panel" id="guide-panel-estimate" role="tabpanel" aria-labelledby="guide-tab-estimate" tabindex="0" hidden><div class="guide-formula featured"><span>已定价订单行的估算销售额</span><strong>Σ（有效销量订单行数量 × 匹配的单位价格）</strong></div><div class="guide-formula"><span>价格覆盖率</span><strong>已定价有效订单行 ÷ 全部有效订单行 × 100%</strong></div><p>取消或不符合销售资格的订单排除。按订单时点匹配价格区间；区间外观察价标记为外推。缺价格的订单行留空，不当作零销售额，也不把已定价样本外推至全部订单。</p><p>订单日期来源与匹配质量会影响估算可靠性；首次抓取时间不等同真实成交时点。价格采集和订单价格回填分别更新，估算只使用已经写入订单行的匹配结果。</p></div>
      <div class="guide-calculation-panel" id="guide-panel-pending" role="tabpanel" aria-labelledby="guide-tab-pending" tabindex="0" hidden><div class="guide-formula featured"><span>快照净待回款</span><strong>销售待回款 ＋ 运费待回款 − 两类冲回绝对值</strong></div><p>待出账面单与待回款来自不同快照，未逐订单对齐，不能将两份总额直接相减为利润。</p><p>遇到非零“已减优惠”回款列时暂停净待回款合计，待确认其是否替代原回款或表示独立交易后再计算。与已到账记录重叠的 PO 单列提醒，不自动扣除。</p></div>
      <div class="guide-rule-grid"><article><i data-lucide="calendar-days"></i><h3>时间</h3><p>上海时区。销量按标准订单日期，结算按到账时间，费用按记账时间。财务总览先筛日期，再按日 / 周 / 月分桶。</p></article><article><i data-lucide="circle-dollar-sign"></i><h3>币种与缺失</h3><p>金额合计以 USD 为口径，出现其他或未知币种时暂停合计。缺账单、缺价格、未接入成本留空，已知零金额才显示 0。</p></article><article><i data-lucide="package-search"></i><h3>商品与退款</h3><p>取消订单、销售冲回、运费冲回及拒付分别计数，不合并为退款件数。商品汇总只包含可关联的结算，不替代完整订单利润。</p></article></div>
      <details class="guide-details"><summary>平台收入与支出的完整来源表 <i data-lucide="chevron-down"></i></summary><div class="guide-table-wrap"><table class="guide-table guide-amount-table"><caption class="guide-sr-only">平台收入和支出来源及计算方向</caption><thead><tr><th>项目</th><th>数据来源</th><th>方向</th><th>状态 / 计算规则</th></tr></thead><tbody>${guideAmounts.map(row => `<tr><td><strong>${row[0]}</strong></td><td>${row[1]}</td><td>${row[2]}</td><td>${guideBadge(row[3])}<p>${row[4]}</p></td></tr>`).join("")}</tbody></table></div></details></section>
    <section class="guide-section" id="guide-settlement"><div class="guide-section-heading"><span>05</span><div><h2>待结算报表如何核对重叠</h2><p>保留快照的原始待处理金额，把需要核对的已到账重叠单列展示。</p></div></div><figure class="guide-settlement-flow">${guideNode("输入", "① 待处理快照", "分别读取待回款与待出账面单。")}${guideArrow("关联")}${guideNode("核对键", "② 同店铺标识", "订单用 PO；面单用包裹、运单、账单类型和备注。")}${guideArrow("比对")}${guideNode("识别重叠", "③ 查询已到账", "查找结算流水 / 聚合 PO 或已出账面单中的对应记录。")}${guideArrow("汇总")}${guideNode("输出", "④ 展示核对项", "按店铺和币种列出重叠数量及原始待处理金额。")}<figcaption>PO 重叠不证明已经全部结清。报表只提示核对，不自动核销，避免误删仍待结算的部分。</figcaption></figure><div class="guide-note"><i data-lucide="scan-line"></i><div><strong>重复导入的更新方式</strong><p>相同业务键的记录在重复导入时更新；面单业务键包含出账状态，因此待出账与已出账不会互相覆盖。导入已到账流水也不会自动删除待处理 PO。</p></div></div></section>
    <section class="guide-section" id="guide-sync"><div class="guide-section-heading"><span>06</span><div><h2>各报表的更新规则</h2><p>分别查看订单库存、账单导入、价格覆盖与待结算核对的数据状态。</p></div><button class="secondary-command" id="guide-refresh" type="button"><i data-lucide="refresh-cw"></i>刷新状态</button></div><div id="guide-live-status" class="guide-live-grid" aria-live="polite"><p>正在读取数据状态…</p></div><div class="guide-rule-grid"><article><i data-lucide="refresh-cw"></i><h3>销售与库存页面</h3><p>立即同步读取订单、库存和配对数据。各步骤独立写入，库存写入失败时保留上次成功快照，订单可能仍有更新。报表刷新读取当前已保存数据。</p></article><article><i data-lucide="file-input"></i><h3>财务页面</h3><p>上传账单后更新对应数据集，再重新汇总财务报表。立即同步不会补充未上传的结算或费用账单，待处理报表以最近导入的快照为准。</p></article><article><i data-lucide="badge-dollar-sign"></i><h3>价格与估算</h3><p>活动采集更新活动和当前价格状态；历史价格区间供订单回填使用。财务估算在回填结果写入订单后更新，覆盖率反映已匹配订单行的占比。</p></article></div></section>
    <footer class="guide-footer">报表说明 · 上海时区 · 金额与数量以各报表的来源、筛选条件和计算口径为准。上方数据状态从系统接口读取。</footer>`;
}


function bindSystemGuide() {
  renderSystemGuide();
  const root = document.getElementById("system-guide-content");
  root.querySelectorAll("[data-open-view]").forEach(button => button.addEventListener("click", () => switchView(button.dataset.openView)));
  root.querySelectorAll("[data-guide-filter]").forEach(button => button.addEventListener("click", () => {
    root.querySelectorAll("[data-guide-filter]").forEach(item => {
      item.classList.toggle("active", item === button);
      item.setAttribute("aria-pressed", String(item === button));
    });
    let visible = 0;
    root.querySelectorAll("[data-guide-source-state]").forEach(row => {
      row.hidden = button.dataset.guideFilter !== "全部" && row.dataset.guideSourceState !== button.dataset.guideFilter;
      if (!row.hidden) visible++;
    });
    document.getElementById("guide-source-count").textContent = `${visible} 类来源`;
  }));
  const tabs = [...root.querySelectorAll("[data-guide-tab]")];
  function selectTab(tab) {
    tabs.forEach(item => {
      const selected = item === tab;
      item.setAttribute("aria-selected", String(selected));
      item.tabIndex = selected ? 0 : -1;
      document.getElementById(item.getAttribute("aria-controls")).hidden = !selected;
    });
  }
  tabs.forEach(tab => {
    tab.addEventListener("click", () => selectTab(tab));
    tab.addEventListener("keydown", event => {
      let index = tabs.indexOf(tab);
      if (event.key === "ArrowRight") index = (index + 1) % tabs.length;
      else if (event.key === "ArrowLeft") index = (index + tabs.length - 1) % tabs.length;
      else if (event.key === "Home") index = 0;
      else if (event.key === "End") index = tabs.length - 1;
      else return;
      event.preventDefault(); selectTab(tabs[index]); tabs[index].focus();
    });
  });
  document.getElementById("guide-print").addEventListener("click", () => window.print());
  let detailsBeforePrint = null;
  window.addEventListener("beforeprint", () => {
    if (state.view !== "system-guide" || detailsBeforePrint) return;
    detailsBeforePrint = [...root.querySelectorAll("details")].map(item => [item, item.open]);
    detailsBeforePrint.forEach(([item]) => { item.open = true; });
  });
  window.addEventListener("afterprint", () => {
    detailsBeforePrint?.forEach(([item, open]) => { item.open = open; });
    detailsBeforePrint = null;
  });
  document.getElementById("guide-refresh").addEventListener("click", loadSystemGuideStatus);
  const status = document.getElementById("source-status");
  const openSyncGuide = async () => {
    const url = new URL(window.location.href);
    url.hash = "guide-sync";
    window.history.replaceState({}, "", url);
    await switchView("system-guide");
  };
  status.addEventListener("click", openSyncGuide);
  status.addEventListener("keydown", event => {
    if (event.key === "Enter" || event.key === " ") { event.preventDefault(); openSyncGuide(); }
  });
  window.addEventListener("hashchange", () => {
    if (state.view === "system-guide") scrollToSystemGuideSection(window.location.hash);
  });
  lucide.createIcons();
}

function scrollToSystemGuideSection(hash) {
  const section = document.getElementById(String(hash).replace(/^#/, ""));
  if (section?.classList.contains("guide-section")) section.scrollIntoView({ block: "start" });
}

function guideTime(value) {
  if (!value || !Number.isFinite(new Date(value).getTime())) return "未有记录";
  return new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(value));
}

async function loadSystemGuideStatus() {
  systemGuideState.controller?.abort();
  const controller = new AbortController();
  systemGuideState.controller = controller;
  const button = document.getElementById("guide-refresh"), root = document.getElementById("guide-live-status");
  button.disabled = true;
  root.innerHTML = '<p class="guide-live-message">正在读取数据状态…</p>';
  try {
    const response = await api("/api/profit/report-status", { signal: controller.signal });
    const data = response.data, sync = data.sync || {};
    const sources = data.sources || [], coverage = data.coverage || [], pending = data.pending || [];
    const imported = sources.map(row => row.imported_at).filter(Boolean).sort().at(-1);
    const settled = sources.filter(row => row.source === "settled").map(row => row.last_at).filter(Boolean).sort().at(-1);
    const lines = coverage.reduce((sum, row) => sum + Number(row.lines || 0), 0);
    const priced = coverage.reduce((sum, row) => sum + Number(row.priced_lines || 0), 0);
    const warning = coverage.reduce((sum, row) => sum + Number(row.warning_lines || 0), 0);
    const overlaps = pending.reduce((sum, row) => sum + Number(row.overlap_orders || 0), 0);
    const inventoryFailure = sync.status === "failed" && sync.error?.includes("warehouse_inventory_pkey");
    const syncTitle = inventoryFailure ? "库存同步失败" : ({ succeeded: "最近同步成功", failed: "本轮有步骤失败", running: "正在同步" }[sync.status] || "暂无同步记录");
    const card = (label, value, detail, kind = "") => `<article class="guide-live-card ${kind}"><span>${escapeHtml(label)}</span><strong>${escapeHtml(value)}</strong><p>${escapeHtml(detail)}</p></article>`;
    root.innerHTML = card("订单与库存同步", syncTitle, `${guideTime(sync.completed_at || sync.started_at)}；本轮同步订单 ${formatNumber(sync.orders_synced || 0)}，商品行 ${formatNumber(sync.lines_synced || 0)}，库存行 ${formatNumber(sync.inventory_synced || 0)}。`, sync.status === "failed" ? "warning" : "")
      + card("财务账单", imported ? "最近导入 " + guideTime(imported) : "尚未导入", `结算到账时间截至 ${guideTime(settled)}；账单由 Excel 导入更新，立即同步不会补充账单。`)
      + card("订单价格", lines ? `${(priced / lines * 100).toFixed(2)}% 覆盖` : "暂无有效订单行", `已定价 ${formatNumber(priced)} / ${formatNumber(lines)} 行；其中 ${formatNumber(warning)} 行有价格匹配警告。`, warning || (lines && !priced) ? "warning" : "")
      + card("待结算检查", `${formatNumber(overlaps)} 个重叠 PO`, "与已到账记录同时存在的待处理订单；仅提示核对，未自动核销。", overlaps ? "warning" : "");
  } catch (error) {
    if (error.name !== "AbortError") root.innerHTML = '<p class="guide-live-message error">状态读取失败，请点击“刷新状态”重试。下方说明仍可阅读。</p>';
  } finally {
    if (systemGuideState.controller === controller) button.disabled = false;
  }
}
