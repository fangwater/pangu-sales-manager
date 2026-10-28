const ledgerOrderNames = { matched: "已关联", unmatched: "未关联", ambiguous: "多个 PO", conflict: "关联冲突" };
function bindFinanceLedger() {
  document.getElementById("finance-ledger-close").addEventListener("click", () => document.getElementById("finance-ledger-dialog").close());
  document.getElementById("finance-ledger-body").addEventListener("click", event => {
    const button = event.target.closest("[data-ledger-detail]");
    if (!button) return;
    document.getElementById("finance-ledger-detail").innerHTML = state.profitLedger.details[Number(button.dataset.ledgerDetail)] || "";
    document.getElementById("finance-ledger-dialog").showModal();
  });
  document.getElementById("finance-ledger-sku-export").addEventListener("click", () => {
    if (!state.profitLedger.data) return;
    financeDownloadCSV("temu-sku-ledger.csv", ["店铺", "MSKU", "币种", "结算净额", "平台费用净额", "收支净额", "来源笔数"], state.profitLedger.data.sku_totals.map(row => [financeShopName(row.shop_key), row.platform_sku, row.currency, row.settlement_amount, row.fee_amount, row.net_amount, row.rows]), "当前费用筛选条件，包含唯一匹配及公共费用分摊，未扣内部成本。");
  });
  document.getElementById("finance-allocation-form").addEventListener("submit", async event => {
    event.preventDefault(); const report = state.profitLedger, button = document.getElementById("finance-allocation-save");
    if (!report.policy) return; button.disabled = true;
    try {
      const response = await api("/api/profit/allocation-policy", { method: "PUT", headers: { "Content-Type": "application/json", "X-Pangu-Request": "finance" }, body: JSON.stringify({ method: document.getElementById("finance-allocation-method").value, version: report.policy.version }) });
      report.policy = response.data; await loadFinanceLedger(1);
    } catch (error) { showError(error.message); } finally { button.disabled = !report.policy; }
  });
  document.getElementById("finance-ledger-form").addEventListener("submit", event => { event.preventDefault(); loadFinanceLedger(1); });
  document.getElementById("finance-ledger-reset").addEventListener("click", () => { document.getElementById("finance-ledger-form").reset(); setFinanceLedgerDates(); loadFinanceLedger(1); });
  document.getElementById("finance-ledger-prev").addEventListener("click", () => loadFinanceLedger(state.profitLedger.page - 1));
  document.getElementById("finance-ledger-next").addEventListener("click", () => loadFinanceLedger(state.profitLedger.page + 1));
  document.getElementById("finance-ledger-export").addEventListener("click", () => {
    if (!state.profitLedger.data) return;
    const link = document.createElement("a"); link.href = `${APP_BASE}/api/profit/ledger/export?${state.profitLedger.params}`; link.download = "temu-order-fees.csv"; link.click();
  });
}
function setFinanceLedgerDates() {
  const today = financeDate(new Date()), first = new Date(today.slice(0, 7) + "-01T00:00:00Z"); first.setUTCDate(0);
  const end = first.toISOString().slice(0, 10);
  document.getElementById("finance-ledger-start").value = end.slice(0, 7) + "-01";
  document.getElementById("finance-ledger-end").value = end;
}
function financeLedgerParams() {
  const form = document.getElementById("finance-ledger-form");
  return new URLSearchParams(new FormData(form));
}
async function loadFinanceLedger(page = 1) {
  if (!document.getElementById("finance-ledger-start").value) setFinanceLedgerDates();
  if (!financeDatesValid("finance-ledger-start", "finance-ledger-end")) return;
  const report = state.profitLedger;
  report.controller?.abort(); const controller = new AbortController(); report.controller = controller;
  report.data = null; report.loaded = false;
  const params = financeLedgerParams(); report.params = params.toString(); params.set("page", page); params.set("page_size", "30");
  document.getElementById("finance-ledger-export").disabled = true;
  document.getElementById("finance-ledger-sku-export").disabled = true;
  document.getElementById("finance-ledger-query").disabled = true;
  document.getElementById("finance-ledger-body").innerHTML = emptyRow(15, "正在读取费用明细");
  document.getElementById("finance-ledger-totals").innerHTML = "";
  document.getElementById("finance-ledger-skus").innerHTML = "";
  ["prev", "next"].forEach(key => document.getElementById(`finance-ledger-${key}`).disabled = true);
  document.getElementById("finance-ledger-count").textContent = "";
  try {
    const [response, policyResponse] = await Promise.all([api(`/api/profit/ledger?${params}`, { signal: controller.signal }), api("/api/profit/allocation-policy", { signal: controller.signal })]);
    if (report.controller !== controller) return;
    report.policy = policyResponse.data;
    document.getElementById("finance-allocation-method").value = report.policy.method;
    document.getElementById("finance-allocation-version").textContent = `版本 ${report.policy.version}`;
    document.getElementById("finance-allocation-save").disabled = false;
    report.data = response.data; report.page = page; report.loaded = true;
    renderFinanceLedger(); showError("");
    document.getElementById("finance-ledger-export").disabled = false;
    document.getElementById("finance-ledger-sku-export").disabled = false;
  } catch (error) {
    if (error.name === "AbortError" || report.controller !== controller) return;
    document.getElementById("finance-ledger-body").innerHTML = emptyRow(15, "费用明细读取失败");
    document.getElementById("finance-ledger-sync").textContent = "";
    showError(error.message);
  } finally { if (report.controller === controller) document.getElementById("finance-ledger-query").disabled = false; }
}
function renderFinanceLedger() {
  const report = state.profitLedger, data = report.data;
  document.getElementById("finance-ledger-totals").innerHTML = data.totals.map(row => `<tr><td>${escapeHtml(financeShopName(row.shop_key))}</td><td>${escapeHtml(row.currency || "缺失")}</td><td class="num">${formatNumber(row.rows)}</td>${financePendingCell(row.amount, row.currency)}<td class="num">${formatNumber(row.order_matched_rows)}</td>${financePendingCell(row.sku_matched_amount, row.currency)}${financePendingCell(row.non_order_amount, row.currency)}${financePendingCell(row.unattributed_amount, row.currency)}<td class="num">${formatNumber(row.unattributed_rows)}</td></tr>`).join("") || emptyRow(9, "没有匹配的费用明细");
  document.getElementById("finance-ledger-skus").innerHTML = data.sku_totals.map(row => `<tr><td>${escapeHtml(financeShopName(row.shop_key))}</td><td>${escapeHtml(row.platform_sku)}</td><td>${escapeHtml(row.currency || "缺失")}</td>${financePendingCell(row.settlement_amount, row.currency)}${financePendingCell(row.fee_amount, row.currency)}${financePendingCell(row.net_amount, row.currency)}<td class="num">${formatNumber(row.rows)}</td></tr>`).join("") || emptyRow(7, "没有已归因的商品收支");
  report.details = [];
  document.getElementById("finance-ledger-body").innerHTML = data.rows.map((row, index) => {
    const product = row.products.find(item => item.platform_sku === row.platform_sku);
    const products = row.products.map(item => `<tr><td>${escapeHtml(item.platform_sku || "缺失")}</td><td>${escapeHtml(item.sku_id ?? "—")}</td><td>${escapeHtml(item.product_name || "—")}</td><td>${escapeHtml(item.variant_name || "—")}</td></tr>`).join("") || emptyRow(4, "无订单商品记录");
    const splits = row.allocations.map(item => `<tr><td>${escapeHtml(item.po_no)}</td><td>${escapeHtml(item.platform_sku)}</td><td>${escapeHtml(item.basis_currency)}</td><td class="num">${item.quantity == null ? "—" : formatNumber(item.quantity)}</td><td class="num">${item.declared_value == null ? "—" : formatNumber(item.declared_value)}</td><td class="num">${item.ratio ? (Number(item.ratio) * 100).toFixed(4)+"%" : "人工金额"}</td>${financePendingCell(item.amount, row.currency)}<td class="num">${escapeHtml(item.rounding_adjustment)}</td><td>${escapeHtml(item.basis_source?.startsWith("manual_review") ? "人工核对" : item.basis_source === "parent_sku" ? "PO 聚合商品" : "销售回款商品")}</td><td>${escapeHtml(item.policy_version)}</td></tr>`).join("") || emptyRow(10, "无公共费用分摊");
    const evidence = row.package_evidence.map(item => `<tr><td>${escapeHtml(item.source === "shipment" ? "发货记录" : "订单详情")}</td><td>${escapeHtml(item.source_key)}</td><td>${escapeHtml(item.child_order_no || "—")}</td><td>${escapeHtml(item.package_no)}</td><td>${escapeHtml(item.tracking_no || "—")}</td><td>${escapeHtml(formatDateTime(item.source_updated_at))}</td></tr>`).join("") || emptyRow(6, "无包裹关联记录");
    report.details.push(`<dl><dt>明细编号</dt><dd>${escapeHtml(row.entry_key)}</dd><dt>原始订单编号</dt><dd>${escapeHtml(row.order_reference || "—")}</dd><dt>候选 PO</dt><dd>${escapeHtml(row.candidate_pos.join("；") || "—")}</dd><dt>原始金额</dt><dd>${escapeHtml(financePendingMoney(row.original_amount, row.currency))}</dd><dt>下单时间</dt><dd>${escapeHtml(formatDateTime(row.order_at))}</dd><dt>到账时间</dt><dd>${escapeHtml(formatDateTime(row.paid_at))}</dd><dt>商品分摊</dt><dd>${escapeHtml(row.allocation_status === "allocated" ? "已分摊，见分摊明细" : "—")}</dd><dt>人工核对</dt><dd>${escapeHtml(row.review?row.review.reference+" · 版本 "+row.review.version:"—")}</dd><dt>备注</dt><dd>${escapeHtml(row.remark || "—")}</dd></dl><div class="table-wrap"><table><thead><tr><th>MSKU</th><th>SKU ID</th><th>商品名称</th><th>属性</th></tr></thead><tbody>${products}</tbody></table></div><div class="table-wrap"><table><thead><tr><th>包裹来源</th><th>来源编号</th><th>子订单号</th><th>包裹号</th><th>运单号</th><th>来源更新时间</th></tr></thead><tbody>${evidence}</tbody></table></div><div class="table-wrap"><table><thead><tr><th>分摊 PO</th><th>MSKU</th><th>申报币种</th><th>数量</th><th>申报金额</th><th>占比</th><th>分摊金额</th><th>尾差调整</th><th>依据</th><th>规则版本</th></tr></thead><tbody>${splits}</tbody></table></div>`);
    return `<tr><td>${escapeHtml(formatDateTime(row.occurred_at))}</td><td>${escapeHtml(financeShopName(row.shop_key))}</td><td>${escapeHtml(row.fee_name)}</td><td>${escapeHtml(row.source_reference)}</td><td>${escapeHtml(row.po_no || "—")}</td><td>${escapeHtml(row.package_no || "—")}</td><td>${escapeHtml(row.tracking_no || "—")}</td><td>${escapeHtml(row.platform_sku || "—")}</td><td>${escapeHtml(product?.product_name || "—")}</td><td>${escapeHtml(row.currency || "缺失")}</td>${financePendingCell(row.amount, row.currency)}<td>${financePill(ledgerOrderNames[row.order_status], row.order_status === "matched" ? "muted" : "warning")}</td><td>${financePill(row.review?.status === "stale" ? "人工核对待复核" : row.allocation_status === "non_order" ? "非订单款项" : row.review?.status === "active" ? "人工归因" : row.allocation_status === "allocated" ? "已分摊" : row.allocation_status === "matched" ? "已归因" : "未归因", row.allocation_status !== "unattributed" ? "muted" : "warning")}</td><td>${escapeHtml(row.allocation_reason || "—")}</td><td><button type="button" class="text-command" data-ledger-detail="${index}">明细</button>${row.allocation_status==='unattributed'||row.review?` <button type="button" class="text-command" data-review-entry="${escapeHtml(row.entry_key)}">核对</button>`:''}</td></tr>`;
  }).join("") || emptyRow(15, "没有匹配的费用明细");
  const pages = Math.max(1, Math.ceil(data.total / data.page_size));
  document.getElementById("finance-ledger-count").textContent = `共 ${formatNumber(data.total)} 条`;
  document.getElementById("finance-ledger-page").textContent = `${report.page} / ${pages}`;
  document.getElementById("finance-ledger-prev").disabled = report.page <= 1;
  document.getElementById("finance-ledger-next").disabled = report.page >= pages;
  const shops = document.getElementById("finance-ledger-shop").value ? [document.getElementById("finance-ledger-shop").value] : ["panda-homes", "panda-buy"];
  document.getElementById("finance-ledger-sync").textContent = shops.map(shop => {
    const sync = data.package_sync.find(row => row.shop_key === shop);
    if (!sync?.succeeded_at) return `${financeShopName(shop)}：包裹数据尚未同步`;
    const failure = sync.error_message ? "；最近同步失败，保留上次成功数据" : "";
    return `${financeShopName(shop)}：包裹同步 ${formatDateTime(sync.succeeded_at)}，${formatNumber(sync.link_count)} 条关联${failure}`;
  }).join("。 ");
}
