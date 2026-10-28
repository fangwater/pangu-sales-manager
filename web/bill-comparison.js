"use strict";
const billState={page:1,data:null,params:'',controller:null,dialog:null};
const billSources={fund_detail:'账务明细 · 结算',settled_flow:'PO 明细',settled_parent:'PO 聚合'};
const billStatuses={matched:'金额一致',difference:'存在差异',missing:'缺少依据',review:'待核对'};
const billComponents=['sales_receipt','freight_receipt','sales_chargeback','freight_chargeback'];
function bindBillComparison(){
 document.getElementById('margin-dialog').addEventListener('close',()=>{billState.dialog=null;});
 document.getElementById('bill-filter').onsubmit=e=>{e.preventDefault();loadBillComparison(1);};
 document.getElementById('bill-reset').onclick=()=>{document.getElementById('bill-filter').reset();loadBillComparison(1);};
 document.getElementById('bill-prev').onclick=()=>loadBillComparison(billState.page-1);
 document.getElementById('bill-next').onclick=()=>loadBillComparison(billState.page+1);
 document.getElementById('bill-export').onclick=()=>{const a=document.createElement('a');a.href=APP_BASE+'/api/profit/bill-comparison?'+billState.params+'&export=1';a.click();};
 document.getElementById('bill-import-open').onclick=openBillImport;
}
async function loadBillComparison(page=1){
 const b=billState;b.controller?.abort();const controller=new AbortController();b.controller=controller;b.data=null;
 const params=new URLSearchParams(new FormData(document.getElementById('bill-filter')));b.params=params.toString();params.set('page',page);
 document.getElementById('bill-query').disabled=true;document.getElementById('bill-export').disabled=true;
 ['prev','next'].forEach(k=>document.getElementById('bill-'+k).disabled=true);
 document.getElementById('bill-body').innerHTML=emptyRow(10,'正在读取账单核对');document.getElementById('bill-summary').textContent='';document.getElementById('bill-files').textContent='';document.getElementById('bill-count').textContent='';
 try{
  const response=await api('/api/profit/bill-comparison?'+params,{signal:controller.signal});if(b.controller!==controller)return;
  b.data=response.data;b.page=page;renderBillComparison();document.getElementById('bill-export').disabled=false;showError('');
 }catch(error){if(error.name!=='AbortError'&&b.controller===controller){showError(error.message);document.getElementById('bill-body').innerHTML=emptyRow(10,error.message);}}
 finally{if(b.controller===controller)document.getElementById('bill-query').disabled=false;}
}
function billAmounts(source,dates=false){return billComponents.map(k=>escapeHtml(source?.[k]??'未提供')).join('<br>')+(dates&&source?`<small>${source.first_at?escapeHtml(formatDateTime(source.first_at))+' 至 '+escapeHtml(formatDateTime(source.last_at)):'账期未提供'}</small>`:'');}
function renderBillComparison(){
 const d=billState.data,s=d.summary;
 document.getElementById('bill-summary').innerHTML=Object.entries(billStatuses).map(([key,label])=>`<span>${label}<strong>${s[key]}</strong></span>`).join('');
 document.getElementById('bill-body').innerHTML=d.rows.map((r,i)=>`<tr><td>${escapeHtml(financeShopName(r.shop_key))}<small>${escapeHtml(r.po_no||'PO 缺失')}</small></td><td>${escapeHtml(r.currency||'缺失')}</td><td>销售回款<br>运费回款<br>销售冲回<br>运费冲回</td><td>${billAmounts(r.fund,true)}</td><td>${billAmounts(r.detail,true)}</td><td>${billAmounts(r.parent,true)}</td><td>${billAmounts(r.deltas.fund_detail)}</td><td>${billAmounts(r.deltas.detail_parent)}</td><td>${billStatuses[r.status]}<small>${escapeHtml(r.reasons.join('；'))}</small><small>流水差异 ${r.flow_differences} 笔</small></td><td><button class="text-command" data-bill-detail="${i}">核对依据</button></td></tr>`).join('')||emptyRow(10,'无匹配账单');
 document.querySelectorAll('[data-bill-detail]').forEach(button=>button.onclick=()=>openBillDetail(d.rows[button.dataset.billDetail]));
 document.getElementById('bill-count').textContent=`第 ${d.page} 页 · ${d.total} 个 PO / 币种`;
 document.getElementById('bill-prev').disabled=d.page<=1;document.getElementById('bill-next').disabled=d.page*d.page_size>=d.total;
 document.getElementById('bill-files').innerHTML=d.files.map(f=>`<tr><td>${escapeHtml(formatDateTime(f.imported_at))}<small>${escapeHtml(financeShopName(f.shop_key))}</small></td><td>${billSources[f.source]}</td><td>${escapeHtml(f.filename)}</td><td>${f.rows}</td><td class="margin-hash">${f.hash_basis==='file'?'文件':'解析内容'} SHA256<small>${escapeHtml(f.sha256)}</small><button class="text-command" data-bill-file="${f.id}">查看依据</button></td></tr>`).join('')||emptyRow(5,'无独立来源依据');
 document.querySelectorAll('[data-bill-file]').forEach(button=>button.onclick=()=>{const f=d.files.find(f=>f.id===Number(button.dataset.billFile));openBillDetail({shop_key:f.shop_key,po_no:'',currency:'',file_id:f.id,filename:f.filename,status:''});});
}
function billDialog(title,body){const token={};billState.dialog=token;document.getElementById('margin-dialog-title').textContent=title;document.getElementById('margin-detail').innerHTML=body;document.getElementById('margin-dialog').showModal();return token;}
function openBillImport(){
 const options=[...document.querySelector('#bill-filter [name=shop_key]').options].filter(o=>o.value).map(o=>`<option value="${escapeHtml(o.value)}">${escapeHtml(o.text)}</option>`).join('');
 const token=billDialog('导入核对依据',`<p class="margin-note">仅保存三类已到账账单的独立核对依据，不更新收入、费用或待处理账单。相同文件跳过；同来源业务键采用最后一次新文件的记录，未出现记录保留。账务明细仅核对“结算”工作表。</p><form id="bill-import-form"><div class="margin-form-grid"><label>店铺<select name="shop_key" required>${options}</select></label><label>文件<input name="file" id="bill-import-file" type="file" accept=".xlsx,.zip" required></label></div><p class="margin-note">XLSX 或仅包含账务明细、PO 明细、PO 聚合账单的 ZIP；上限 32 MB。旧账单未保留独立来源时显示缺少依据，须从原文件补入。</p><p class="margin-note" id="bill-import-message" role="status"></p><button class="primary-command" id="bill-import-save">导入依据</button></form>`);
 const selected=document.querySelector('#bill-filter [name=shop_key]').value;if(selected)document.querySelector('#bill-import-form [name=shop_key]').value=selected;
 document.getElementById('bill-import-form').onsubmit=async e=>{
  e.preventDefault();const form=e.target,button=document.getElementById('bill-import-save'),message=document.getElementById('bill-import-message');
  if(form.file.files[0].size>32*1024*1024){message.textContent='上传文件不能超过 32 MB';return;}
  const data=new FormData(form);[...form.elements].forEach(el=>el.disabled=true);button.disabled=true;message.textContent='正在导入核对依据';
  try{const response=await api('/api/profit/bill-evidence/import',{method:'POST',headers:{'X-Pangu-Request':'finance'},body:data});if(billState.dialog!==token||!document.getElementById('margin-dialog').open)return;const r=response.data;message.textContent=`新增 ${r.files} 个文件、${r.rows} 条依据，跳过 ${r.unchanged_files} 个相同文件`;await loadBillComparison(1);}
  catch(error){if(billState.dialog===token)message.textContent=error.message;}
  finally{if(billState.dialog===token)[...form.elements].forEach(el=>el.disabled=false);}
 };
}
async function openBillDetail(row,page=1,token=null){
 if(!token)token=billDialog('账单核对依据',`<p class="margin-note">${escapeHtml(financeShopName(row.shop_key))} · ${escapeHtml(row.file_id?row.filename:row.po_no||'PO 缺失')} · ${row.file_id?'文件 '+row.file_id:escapeHtml(row.currency||'币种缺失')+' · '+billStatuses[row.status]}</p><p class="margin-note">${escapeHtml((row.reasons||[]).join('；'))}</p><div id="bill-detail-content"></div>`);
 const target=document.getElementById('bill-detail-content');target.textContent='正在读取核对依据';
 try{
  const params=new URLSearchParams({shop_key:row.shop_key,po_no:row.po_no,currency:row.currency,page,...(row.file_id?{file_id:row.file_id}:{})});
  const response=await api('/api/profit/bill-comparison/detail?'+params);if(billState.dialog!==token||!document.getElementById('margin-dialog').open)return;const d=response.data;
  target.innerHTML=`<div class="table-scroll"><table><thead><tr><th>来源 / 业务键</th><th>原币款项</th><th>到账时间</th><th>原文件 / 序号</th><th>SHA256 / 原始字段</th></tr></thead><tbody>${d.rows.map(r=>`<tr><td>${billSources[r.source]}<small>${escapeHtml(r.business_key)}</small><small>${escapeHtml(r.po_no)} · ${escapeHtml(r.currency||'币种缺失')}</small></td><td>${r.source==='settled_parent'?`销售回款 ${escapeHtml(r.original_sales_receipt??'未提供')}<br>运费回款 ${escapeHtml(r.original_freight_receipt??'未提供')}<br>销售冲回 ${escapeHtml(r.original_sales_chargeback??'未提供')}<br>运费冲回 ${escapeHtml(r.original_freight_chargeback??'未提供')}`:`${escapeHtml(r.trade_type)}<br>${escapeHtml(r.original_amount??'未提供')}`}</td><td>${escapeHtml(r.received_at?formatDateTime(r.received_at):'未提供')}</td><td>${escapeHtml(r.filename)}<small>明细序号 ${r.row_no} · 文件 ${r.file_id}</small></td><td class="margin-hash">${escapeHtml(r.sha256)}<details><summary>原始字段</summary><pre class="bill-payload">${escapeHtml(JSON.stringify(r.payload,null,2))}</pre></details></td></tr>`).join('')||emptyRow(5,'无独立来源依据')}</tbody></table></div><div class="finance-pagination"><span>第 ${d.page} 页 · ${d.total} 条</span><div><button class="secondary-command" id="bill-detail-prev" ${d.page<=1?'disabled':''}>上一页</button><button class="secondary-command" id="bill-detail-next" ${d.page*d.page_size>=d.total?'disabled':''}>下一页</button></div></div>`;
  document.getElementById('bill-detail-prev').onclick=()=>openBillDetail(row,page-1,token);document.getElementById('bill-detail-next').onclick=()=>openBillDetail(row,page+1,token);
 }catch(error){if(billState.dialog===token)target.textContent=error.message;}
}
