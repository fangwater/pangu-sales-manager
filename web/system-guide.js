const systemGuideState = { controller: null };

const guideSources = [
  ["已出账", "对账中心－账务明细", "结算；拒付；履约违规 1 / 2；平台退货面单承担；处置费", "分工作表保存。结算流水参与回款，其他工作表参与费用收支。", "已实现"],
  ["已出账", "结算数据－已到账款项－PO 明细账单", "流水 ID、PO、交易类型、到账金额与时间", "与账务明细的结算流水共用同一数据集，按店铺＋流水 ID 去重。", "已实现"],
  ["已出账", "结算数据－已到账款项－PO 聚合账单", "PO、商品行、销售 / 运费回款及冲回", "保存用于订单和商品核对；聚合金额不再次叠加进结算总额。", "已实现"],
  ["已出账", "发货面单费－已出账", "包裹号、运单号、账单类型、费用与记账时间", "进入已出账费用；包裹 / 运单到订单与 SKU 的完整关联尚未完成。", "部分实现"],
  ["已出账", "退货面单费－退至商家仓", "资金账单 ID、PO、运单号、金额与时间", "进入商家仓退货费用；商品级归因仍待完善。", "部分实现"],
  ["已出账", "退货面单费－退至第三方仓", "资金账单 ID、PO、运单号、金额与时间", "进入第三方仓退货费用，与商家仓分开展示。", "部分实现"],
  ["未出账", "结算数据－待处理款项", "PO、商品数量、申报金额、待回款与冲回", "作为待处理快照；检查与已到账 PO 的重叠，不自动核销。", "部分实现"],
  ["未出账", "发货面单费－待出账", "包裹号、运单号、预估面单费", "独立保存预估费用；按包裹、运单和账单信息检查跨状态重叠。", "部分实现"],
];

const guideAmounts = [
  ["销售回款", "账务明细 · 结算 / PO 明细", "收入", "已接入", "按交易类型汇总结算流水。"],
  ["运费回款", "账务明细 · 结算 / PO 明细", "收入", "已接入", "运费收入单列，不与销售回款混为同一笔。"],
  ["销售冲回", "结算流水", "扣减", "已接入", "从销售回款中扣除；Word 的“履约违规 2”引用需修正。"],
  ["运费冲回", "结算流水", "扣减", "已接入", "从运费回款中扣除；与销售冲回分别计数。"],
  ["延迟到货", "账务明细 · 履约违规 1", "支出", "已接入", "按负的绝对值汇总，原始导入金额保留。"],
  ["虚假发货", "账务明细 · 履约违规 2", "支出", "已接入", "按负的绝对值汇总，与冲回属于不同费用。"],
  ["买家拒付", "账务明细 · 支出－买家拒付", "支出", "已接入", "按负的绝对值汇总，笔数不当作退款件数。"],
  ["发货面单费", "发货面单费－已出账", "按账单符号", "已接入", "费用保留账单正负方向；待出账面单不计入此处。"],
  ["商家仓退货面单费", "退货面单费－退至商家仓", "按账单符号", "已接入", "独立分类，避免与第三方仓费用混淆。"],
  ["第三方仓退货面单费", "退货面单费－退至第三方仓", "按账单符号", "已接入", "独立分类，保留资金账单 ID 供核对。"],
  ["平台承担退货面单费", "账务明细 · 其他－退货面单费平台承担", "按账单符号", "已接入", "当前样本为正的补偿款，单独展示，不再次扣作费用。"],
  ["处置费", "账务明细 · 支出－处置费", "支出", "已补入", "Word 的支出清单未列出；现在按负的绝对值计入。"],
  ["税金代扣", "当前样本未提供对应数据源", "待确定", "未接入", "留空，不以零代替。"],
  ["税金退回", "当前样本未提供对应数据源", "待确定", "未接入", "留空，需确认来源及收入 / 冲减口径。"],
  ["非订单交易费", "当前样本未提供对应数据源", "待确定", "未接入", "留空，不能直接归入某个 SKU。"],
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

function renderSystemGuide() {
  document.getElementById("system-guide-content").innerHTML = `
    <div class="guide-hero">
      <div><span class="guide-kicker">SYSTEM GUIDE / TEMU</span><h2>从原始数据，到可解释的报表</h2><p>依据《TEMU数据来源.docx》与当前实现整理。了解每个金额来自哪里、如何计算，以及哪些环节仍需补齐。</p></div>
      <button class="secondary-command" id="guide-print" type="button"><i data-lucide="printer"></i>打印说明</button>
    </div>
    <div class="guide-principles"><div><strong>8 类账单</strong><span>已出账与未出账分开保存</span></div><div><strong>3 条数据链路</strong><span>订单库存 · 财务账单 · 活动价格</span></div><div><strong>3 种金额口径</strong><span>已到账 · 辅助估算 · 待结算</span></div></div>
    <nav class="guide-toc" aria-label="说明目录"><a href="#guide-flow">数据流</a><a href="#guide-sources">来源清单</a><a href="#guide-calculation">计算逻辑</a><a href="#guide-settlement">结算回补</a><a href="#guide-implementation">实现评估</a><a href="#guide-sync">同步状态</a></nav>

    <section class="guide-section" id="guide-flow"><div class="guide-section-heading"><span>01</span><div><h2>三条链路，各自更新</h2><p>立即同步更新订单、库存与 SKU 映射；财务账单需要导入，活动价格由独立任务采集。</p></div></div>
      <figure class="guide-diagram"><div class="guide-flow-head"><span>数据来源</span><span>处理逻辑</span><span>报表输出</span></div>
        <div class="guide-flow-row">${guideNode("订单与库存", "TEMU / SHEIN / XLWMS", "平台订单、仓库库存及 SKU 配对。")}${guideArrow("同步")}${guideNode("标准化", "订单与仓库 SKU", "按平台、店铺、订单去重；通过 SKU 映射和换算系数统一仓库销量。")}${guideArrow("汇总")}${guideNode("销售分析", "销量 · 库存 · 映射覆盖", "订单同步与库存写入分别处理；单步失败不代表所有数据停更。")}</div>
        <div class="guide-flow-row">${guideNode("财务账单", "TEMU 的 8 类 Excel", "在账单导入页选择店铺，上传 XLSX 或含 XLSX 的 ZIP。")}${guideArrow("导入")}${guideNode("业务键去重", "结算、费用与待处理分开", "结算按店铺＋流水 ID 更新；聚合账单用于核对，待处理保留为独立快照。")}${guideArrow("计算")}${guideNode("财务分析", "回款 · 费用 · 待结算", "已到账金额来自账单；待结算不并入已到账金额，内部成本尚未接入。")}</div>
        <div class="guide-flow-row">${guideNode("活动价格", "TEMU 活动与 SKU 价格", "保存生效活动、价格观察与时间区间。")}${guideArrow("匹配")}${guideNode("订单价格回填", "订单时点 × 商品 SKU", "优先匹配对应时间区间；外推价格标为警告，无价格的订单行保留缺失。")}${guideArrow("估算")}${guideNode("辅助估算", "已定价销售额 · 覆盖率", "只计算已定价订单行，不外推至全部销量；不能替代实际回款。")}</div>
        <figcaption>实线表示已经存在的处理路径。后续图中的虚线表示待完成步骤。</figcaption>
      </figure>
      <div class="guide-links"><button type="button" data-open-view="orders">查看标准订单 <i data-lucide="arrow-up-right"></i></button><button type="button" data-open-view="profit">导入财务账单 <i data-lucide="arrow-up-right"></i></button><button type="button" data-open-view="sku-prices">查看 SKU 价格 <i data-lucide="arrow-up-right"></i></button></div>
    </section>

    <section class="guide-section" id="guide-sources"><div class="guide-section-heading"><span>02</span><div><h2>Word 的数据来源，如何进入系统</h2><p>来源是否已接入，与是否已经完成订单 / SKU 归因，是两个不同问题。</p></div></div>
      <div class="guide-filter" role="group" aria-label="按来源实现状态筛选"><button class="active" aria-pressed="true" data-guide-filter="全部" type="button">全部 8 类</button><button aria-pressed="false" data-guide-filter="已实现" type="button">已实现</button><button aria-pressed="false" data-guide-filter="部分实现" type="button">部分实现</button><span id="guide-source-count" aria-live="polite">8 类来源</span></div>
      <div class="guide-table-wrap"><table class="guide-table"><caption class="guide-sr-only">八类 TEMU 账单的数据来源和实现状态</caption><thead><tr><th>阶段 / 文件</th><th>提供的数据</th><th>当前处理</th><th>实现状态</th></tr></thead><tbody>${guideSources.map(row => `<tr data-guide-source-state="${row[4]}"><td><small>${row[0]}</small><strong>${row[1]}</strong></td><td>${row[2]}</td><td>${row[3]}</td><td>${guideBadge(row[4])}</td></tr>`).join("")}</tbody></table></div>
      <div class="guide-note"><i data-lucide="layers"></i><div><strong>三种结算展示，共用流水基准</strong><p>2026-10-07 样本核对：账务明细有 15,145 条结算流水；PO 明细的 7,669 条全部包含于其中，同一流水 ID 的金额无差异。聚合账单的 3,723 个 PO 也均在流水中，但三种文件覆盖范围并不完全相同。把三份金额直接相加会重复累计。</p></div></div>
      <details class="guide-details"><summary>基础信息与费用的关联方式 <i data-lucide="chevron-down"></i></summary><div><p>Word 的“基础信息 9 列”实际列举了 11 项：订单编号、产品 ID、店铺 / 站点、费用名、费用时间、付款时间、MSKU、MSKU 属性、产品名称、包裹号、运单号。</p><p>订单 API 和标准订单已提供部分订单及商品信息；完整的“订单 → 商品 → 包裹 / 运单 → 费用 → 付款时间”明细链尚未形成。发货面单不能仅凭 SKU 推算，应先用包裹 / 运单找到订单，再确定商品费用分摊规则。</p></div></details>
    </section>

    <section class="guide-section" id="guide-calculation"><div class="guide-section-heading"><span>03</span><div><h2>选择金额口径，查看计算逻辑</h2><p>到账事实、价格估算和待处理快照各有自己的数据与时间口径。</p></div></div>
      <div class="guide-tabs" role="tablist" aria-label="金额口径"><button id="guide-tab-posted" role="tab" aria-selected="true" aria-controls="guide-panel-posted" tabindex="0" data-guide-tab="posted" type="button">已到账报表</button><button id="guide-tab-estimate" role="tab" aria-selected="false" aria-controls="guide-panel-estimate" tabindex="-1" data-guide-tab="estimate" type="button">活动价辅助估算</button><button id="guide-tab-pending" role="tab" aria-selected="false" aria-controls="guide-panel-pending" tabindex="-1" data-guide-tab="pending" type="button">待结算快照</button></div>
      <div class="guide-calculation-panel" id="guide-panel-posted" role="tabpanel" aria-labelledby="guide-tab-posted" tabindex="0"><div class="guide-formula"><span>结算回款净额</span><strong>销售回款 ＋ 运费回款 − 销售冲回绝对值 − 运费冲回绝对值</strong></div><div class="guide-formula featured"><span>平台收支净额</span><strong>结算回款净额 ＋ 已导入的平台费用收支</strong><small>支出按负数相加，平台补偿保留账单方向；采购、头程等内部成本未扣除。</small></div><div class="guide-example"><span>计算示例 · 非真实订单</span><p>销售 100 ＋ 运费 20 − 销售冲回 5 − 运费冲回 1 ＝ 净结算 114；面单 −30、违规 −2、补偿 ＋3，平台收支净额为 <strong>85</strong>。因为缺少内部成本，不能据此得出毛利。</p></div></div>
      <div class="guide-calculation-panel" id="guide-panel-estimate" role="tabpanel" aria-labelledby="guide-tab-estimate" tabindex="0" hidden><div class="guide-formula featured"><span>已定价订单行的估算销售额</span><strong>Σ（有效销量订单行数量 × 匹配的单位价格）</strong></div><div class="guide-formula"><span>价格覆盖率</span><strong>已定价有效订单行 ÷ 全部有效订单行 × 100%</strong></div><p>取消或不符合销售资格的订单排除。订单时间落在价格区间内时按区间匹配；使用区间外观察价时标记为外推。没有价格的行不当作零销售额，也不把少量已定价样本外推至全部订单。</p><p>当前 TEMU 订单日期主要使用首次抓取时间，尚不能精确代表成交时点。活动采集持续更新，并不意味着历史订单已自动完成价格回填；当前全部已定价行仍为外推警告。</p></div>
      <div class="guide-calculation-panel" id="guide-panel-pending" role="tabpanel" aria-labelledby="guide-tab-pending" tabindex="0" hidden><div class="guide-formula featured"><span>快照净待回款</span><strong>销售待回款 ＋ 运费待回款 − 两类冲回绝对值</strong></div><p>待出账面单费来自另一份快照，未与待回款逐订单对齐，因此不能把两份总额直接相减为利润。</p><p>“已减优惠”的销售 / 运费回款列，Word 尚未说明是替代金额还是独立交易；当前样本为零。遇到非零折后列时暂停净待回款合计，等待口径确认。与已结算数据重叠的 PO 单列提醒，不自动扣除。</p></div>
      <div class="guide-rule-grid"><article><i data-lucide="calendar-days"></i><h3>时间</h3><p>上海时区。销量按标准订单日期；结算按到账时间；费用按记账时间。先筛日期，再按日 / 周 / 月分桶，不能把不同时点的金额直接视为同一订单利润。</p></article><article><i data-lucide="circle-dollar-sign"></i><h3>币种与缺失</h3><p>当前金额合计以 USD 为口径。出现其他或未知币种时暂停 USD 金额合计；缺账单、缺价格、未接入成本留空，已知零金额才显示 0。</p></article><article><i data-lucide="package-search"></i><h3>SKU 与退款</h3><p>SKU 页面只汇总已能关联的商品数据，尚未完整分摊运费和订单费用。取消订单、销售冲回、运费冲回及拒付分别计数，不相加为退款件数。</p></article></div>
      <details class="guide-details"><summary>平台收入与支出的完整来源表 <i data-lucide="chevron-down"></i></summary><div class="guide-table-wrap"><table class="guide-table guide-amount-table"><caption class="guide-sr-only">平台收入和支出来源及计算方向</caption><thead><tr><th>项目</th><th>数据来源</th><th>方向</th><th>状态 / 计算规则</th></tr></thead><tbody>${guideAmounts.map(row => `<tr><td><strong>${row[0]}</strong></td><td>${row[1]}</td><td>${row[2]}</td><td>${guideBadge(row[3])}<p>${row[4]}</p></td></tr>`).join("")}</tbody></table></div></details>
      <div class="guide-links"><button type="button" data-open-view="profit-summary">查看财务总览 <i data-lucide="arrow-up-right"></i></button><button type="button" data-open-view="profit-sku">查看 SKU 财务分析 <i data-lucide="arrow-up-right"></i></button></div>
    </section>

    <section class="guide-section" id="guide-settlement"><div class="guide-section-heading"><span>04</span><div><h2>从待处理到已结算，回补还缺什么</h2><p>Word 要求已出账后覆盖预估。当前完成导入与重叠检查，尚未完成自动回补。</p></div></div>
      <figure class="guide-settlement-flow">${guideNode("已实现", "① 保留两类快照", "待回款与待面单分别导入。")}${guideArrow("检查")}${guideNode("已实现", "② 标记已到账重叠", "PO 比对；面单按包裹、运单与账单信息比对。")}${guideArrow("待完善", true)}${guideNode("待实现", "③ 逐交易核对", "区分部分结算、全部结算及后续冲回。", "pending")}${guideArrow("待完善", true)}${guideNode("待实现", "④ 回补与保留差额", "用已到账替换对应预估，保留仍待结算部分与核对记录。", "pending")}<figcaption>同一个 PO 出现在两边，只能证明需要核对。自动删除整个 PO 会误删尚未结清的部分。</figcaption></figure>
      <div class="guide-note"><i data-lucide="scan-line"></i><div><strong>导入去重与跨状态回补不同</strong><p>重复导入会按业务键更新同一条记录；待出账与已出账面单的业务键包含出账状态，因此不会互相覆盖。待处理订单也不会因导入结算流水而被自动删除。</p></div></div>
      <div class="guide-links"><button type="button" data-open-view="profit-unsettled">查看待结算重叠 <i data-lucide="arrow-up-right"></i></button></div>
    </section>

    <section class="guide-section" id="guide-implementation"><div class="guide-section-heading"><span>05</span><div><h2>当前实现与 Word 要求的差距</h2><p>以下状态说明当前功能边界；待实现步骤不会在示意图里画成已完成。</p></div></div>
      <div class="guide-assessment-grid"><article class="ready"><span>已实现 / 已修正</span><h3>账单汇总可追溯</h3><ul><li>8 类导入、同店铺结算流水去重。</li><li>销售及运费回款、冲回、主要平台费用分类。</li><li>违规 / 拒付支出统一方向，处置费补入汇总。</li><li>筛选、导出、价格覆盖与待结算重叠检查。</li></ul></article><article class="pending"><span>部分实现 / 待完善</span><h3>订单到费用的关联</h3><ul><li>包裹 / 运单到订单及 SKU 的完整关联。</li><li>真实订单时间与持续价格回填。</li><li>真实退款订单数、退款商品件数。</li><li>已出账与未出账逐交易回补核销。</li></ul></article><article class="missing"><span>尚未接入</span><h3>完整利润所需数据</h3><ul><li>税金代扣、税金退回、非订单交易费。</li><li>Word 要求的采购成本、头程费用、物流运费。</li><li>成本与面单费用的边界，避免重复扣除。</li><li>订单 / SKU 毛利润与毛利率。</li></ul></article></div>
      <details class="guide-details" open><summary>需要修正文档的内容 <i data-lucide="chevron-down"></i></summary><div><ol><li><strong>冲回来源：</strong>销售冲回和运费冲回应来自结算流水；“履约违规 2”对应虚假发货支出。</li><li><strong>费用清单：</strong>实际账单包含处置费，Word 清单遗漏；平台承担退货面单费应按补偿的实际方向计算。</li><li><strong>字段数量：</strong>“基础信息 9 列”列举了 11 项，应重新编号，并明确订单、费用及付款时间。</li><li><strong>结算覆盖：</strong>三种结算文件展示有重合但范围不同，应按流水基准去重，不假定完全对应。</li><li><strong>待处理口径：</strong>需明确折后列的含义、部分结算的差额和费用归属，再实现自动回补。</li></ol></div></details>
    </section>

    <section class="guide-section" id="guide-sync"><div class="guide-section-heading"><span>06</span><div><h2>数据链路状态，如何判断影响</h2><p>下面读取当前系统状态；账单导入时间、价格回填与订单库存同步分别展示。</p></div><button class="secondary-command" id="guide-refresh" type="button"><i data-lucide="refresh-cw"></i>刷新状态</button></div>
      <div id="guide-live-status" class="guide-live-grid" aria-live="polite"><p>正在读取数据状态…</p></div>
      <div class="guide-note warning"><i data-lucide="database"></i><div><strong>当前库存同步失败的原因 · 2026-10-07 核对</strong><p>XLWMS 库存源有 3 组同仓库、同 SKU 的记录，分别属于两种库存类型。当前同步读入时未保留库存类型，而库存目标只允许每组仓库＋SKU 保存一行，导致重复写入冲突。库存更新在同一事务中回滚，保留上次成功快照。</p><p>订单与库存分别写入；已有订单同步数量表示本轮订单部分确实更新。财务 Excel 导入和活动价格采集属于独立链路。修复库存前，需要明确不同库存类型应如何聚合，避免随意去重或重复累计数量。</p></div></div>
      <div class="guide-next"><strong>形成完整利润报表的后续顺序</strong><ol><li>补齐最近月份及另一店铺的账单，修复库存同步。</li><li>补准确订单时间，持续回填价格并与已结算样本验证。</li><li>完成订单费用归因、真实退款数量与结算回补。</li><li>统一采购、头程与物流成本口径，再计算毛利。</li></ol></div>
    </section>
    <footer class="guide-footer">说明基线：2026-10-07 · 原始依据：《TEMU数据来源.docx》、样本 Excel 与当前计算实现。数据状态由系统接口实时读取。</footer>`;
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
