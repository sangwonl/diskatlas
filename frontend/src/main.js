import './style.css';
import { Scan, StorageInfo, ScanStorage, PreviewCleanup, ExecuteCleanup, RevealPath } from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';

const $ = (selector) => document.querySelector(selector);
const escape = (value) => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const bytes = (n) => { let value = Number(n || 0), i = 0; const units = ['B','KB','MB','GB','TB']; while(value >= 1024 && i < 4) {value /= 1024; i++;} return `${value.toFixed(i ? 1 : 0)} ${units[i]}`; };
const types = {
 video: ['동영상', '#a777cf'], photos: ['사진', '#dcaa52'], audio: ['음악', '#df7e91'],
 documents: ['문서', '#6294cf'], archives: ['압축 파일·설치 파일', '#79a391'],
 development: ['개발 파일', '#7484c4'], other: ['기타 파일', '#a2a8b3'],
 cache: ['캐시·빌드 파일', '#7484c4'],
};
const tierName = {safe:'다시 생성 가능', caution:'복구 비용 확인', review:'직접 확인 필요', protected:'보호됨'};
let items = new Map(), expanded = new Set(), busy = false, scanValid = false, timer, storage;
let pending = null;
$('#app').innerHTML = `
 <header><strong>Shed</strong><button id="refresh">분석 시작</button></header>
 <main>
  <section class="storage" aria-label="디스크 사용량">
   <div class="storage-heading"><h1>저장 공간</h1><span id="capacity">용량 확인 중…</span></div>
   <div id="bar" class="storage-bar" aria-label="파일 종류별 디스크 사용 비율"></div>
   <div id="legend" class="legend"></div>
   <p id="scope" class="caption"></p>
  </section>
  <div class="list-heading"><h2>정리할 항목</h2><span id="count"></span></div>
  <p id="progress" class="caption" role="status"></p>
  <div id="error" role="alert"></div>
  <div id="list"></div>
 </main>
 <dialog id="review"><div id="review-content"></div></dialog>`;

function category(item) {
 if (!item.ruleId.startsWith('general.')) return 'cache';
 const ext = item.path.split('.').pop().toLowerCase();
 for (const [id, extensions] of Object.entries({
  video:'mp4 mov mkv avi webm m4v', photos:'jpg jpeg png heic gif webp raw tiff psd',
  audio:'mp3 m4a wav flac aiff ogg', documents:'pdf doc docx xls xlsx ppt pptx txt epub pages numbers',
  archives:'zip gz tar 7z rar dmg iso pkg', development:'go js ts tsx jsx py rs swift java c cpp h'
 })) if (extensions.split(' ').includes(ext)) return id;
 return 'other';
}
function drawStorage(value) {
 storage = value;
 const total = Number(value.total), free = Number(value.available), used = Math.max(0, total-free);
 $('#capacity').textContent = `${bytes(total)} 중 ${bytes(used)} 사용 · ${bytes(free)} 남음`;
 const categories = value.categories || [];
 const measured = categories.reduce((sum,c) => sum+c.bytes,0);
 // Shared APFS extents can make per-file totals exceed volume usage.
 const comparable = measured <= used;
 const segments = comparable ? categories.map(c => ({id:c.id, value:c.bytes})) : [];
 segments.push({id:'unclassified', value:comparable ? Math.max(0,used-measured) : used}, {id:'free',value:free});
 const labels = {...types, unclassified:['시스템 및 미분류','#c4c7ce'],free:['사용 가능','#eaecf0']};
 $('#bar').innerHTML = segments.filter(s=>s.value>0).map(s => `<span style="width:${total ? s.value/total*100 : 0}%;background:${labels[s.id][1]}" title="${labels[s.id][0]} ${bytes(s.value)}"></span>`).join('');
 $('#legend').innerHTML = segments.filter(s=>s.value>0).map(s => `<span><i style="background:${labels[s.id][1]}"></i>${labels[s.id][0]} <b>${bytes(s.value)}</b></span>`).join('');
 $('#scope').textContent = !comparable ? '공유 블록 때문에 파일별 합계와 디스크 사용량이 다릅니다. 전체 사용량만 표시합니다.'
  : `시작 디스크 기준 · 파일 종류는 사용자 폴더에서 읽을 수 있는 파일 기준${value.skipped ? ' · 접근하지 못한 항목 '+value.skipped+'개' : ''}${value.complete ? '' : ' · 분류 중'}`;
}
function grouped() {
 const groups = new Map();
 for (const item of items.values()) {
  const id = category(item);
  if (!groups.has(id)) groups.set(id,[]);
  groups.get(id).push(item);
 }
 return [...groups].map(([id,rows])=>({id,rows:rows.sort((a,b)=>b.bytes-a.bytes)}))
  .sort((a,b)=>b.rows.reduce((s,i)=>s+i.bytes,0)-a.rows.reduce((s,i)=>s+i.bytes,0));
}
function render() {
 $('#count').textContent = items.size ? `${items.size}개` : '';
 const groups = grouped();
 $('#list').innerHTML = groups.length ? groups.map(({id,rows}) => {
  const visible = expanded.has(id) ? rows : rows.slice(0,3);
  return `<section class="file-group">
   <div class="group-heading"><h3><i style="background:${types[id][1]}"></i>${types[id][0]} <small>${rows.length}</small></h3>
   <span class="group-total">${bytes(rows.reduce((s,i)=>s+i.bytes,0))}</span>
   <button class="text-button" data-group="${id}" ${!scanValid || busy || rows.every(i=>i.tier==='protected')?'disabled':''}>그룹 정리…</button></div>
   ${visible.map(item=>`<div class="file-row"><div class="file-info"><strong>${escape(item.path.split(/[\\/]/).pop())}</strong>
    <span class="path" title="${escape(item.path)}">${escape(item.path)}</span>
    <details><summary>${tierName[item.tier]}</summary><p>${escape(item.explanation)}</p><p>${escape(item.rebuild)}</p>${item.native?`<p>${escape(item.native)}</p>`:''}</details></div>
    <span class="file-size">${bytes(item.bytes)}</span>
    <button data-item="${escape(item.id)}" ${!scanValid || busy || item.tier==='protected'?'disabled':''}>정리…</button></div>`).join('')}
   ${rows.length>3?`<button class="more" data-expand="${id}">${expanded.has(id)?'접기':`나머지 ${rows.length-3}개 보기`}</button>`:''}
  </section>`;
 }).join('') : `<p class="empty">${busy?'정리할 항목을 찾고 있습니다.':scanValid?'정리 후보가 없습니다.':'분석을 시작하면 정리 후보가 표시됩니다.'}</p>`;
 document.querySelectorAll('[data-expand]').forEach(button=>button.onclick=()=>{const id=button.dataset.expand;expanded.has(id)?expanded.delete(id):expanded.add(id);render();});
 document.querySelectorAll('[data-item]').forEach(button=>button.onclick=()=>review([items.get(button.dataset.item)]));
 document.querySelectorAll('[data-group]').forEach(button=>button.onclick=()=>review(groups.find(g=>g.id===button.dataset.group).rows.filter(i=>i.tier!=='protected')));
}
function review(rows) {
 pending = null;
 const risk = rows.some(i=>i.tier!=='safe');
 $('#review-content').innerHTML = `<h2>${rows.length}개 항목 정리</h2><p>선택한 파일 ${bytes(rows.reduce((s,i)=>s+i.bytes,0))}. 실제 확보량은 공유 블록 등에 따라 다를 수 있습니다.</p>
 <ul>${rows.map(i=>`<li><span>${escape(i.path)}</span><button class="location" data-reveal="${escape(i.id)}">위치 보기</button></li>`).join('')}</ul>
 <p>삭제한 파일은 되돌릴 수 없습니다. 삭제 전에 각 파일의 위치와 내용을 확인하세요.</p>
 ${risk?'<label class="risk"><input id="ack" type="checkbox"> 선택한 파일의 내용을 확인했고, 삭제 후 복구할 수 없음을 이해했습니다.</label>':''}
 <p id="review-error" role="alert"></p><div id="confirmation"></div>
 <footer><button id="cancel">취소</button><button id="prepare" class="primary">계속</button></footer>`;
 $('#review').showModal();
 $('#cancel').onclick=()=>$('#review').close();
 $('#prepare').onclick=async()=>{
  if(risk && !$('#ack').checked){$('#review-error').textContent='선택한 파일을 확인해 주세요.';return;}
  const request={ids:rows.map(i=>i.id),mode:'delete',acknowledgeRisk:risk,confirmation:''};
  $('#prepare').disabled=true;
  try{
   const preview=await PreviewCleanup(request);
   if(preview.blocked.length || !preview.items.length) throw new Error(preview.blocked.map(i=>i.reason).join('\n') || '정리 가능한 항목이 없습니다.');
   pending=request;
   $('#confirmation').innerHTML=`<label>확인: <strong>${escape(preview.confirmationToken)}</strong><input id="token" autocomplete="off" placeholder="위 확인 문구를 입력하세요"></label>`;
   $('#prepare').textContent='확인 후 실행';
   $('#token').oninput=()=>{$('#prepare').disabled=$('#token').value!==preview.confirmationToken;};
   $('#prepare').onclick=async()=>{
    $('#prepare').disabled=true; $('#cancel').disabled=true;
    try{
     const result=await ExecuteCleanup({...pending,confirmation:$('#token').value});
     scanValid=false;
     $('#review').close();
     await refresh();
     $('#error').textContent=result.failed.length ? result.failed.map(i=>i.reason).join(' · ') : `${result.completed.length}개 처리 완료`;
    }catch(err){$('#review-error').textContent=String(err);$('#cancel').disabled=false;}
   };
 }catch(err){$('#review-error').textContent=String(err);$('#prepare').disabled=false;}
 };
 document.querySelectorAll('[data-reveal]').forEach(button=>button.onclick=async()=>{
  button.disabled=true;
  try { await RevealPath(items.get(button.dataset.reveal).path); }
  catch (err) { $('#review-error').textContent=String(err); }
  finally { button.disabled=false; }
 });
}
function schedule(){if(!timer)timer=setTimeout(()=>{timer=null;render();},200);}
async function refresh(){
 if(busy)return;
 busy=true;scanValid=false;items.clear();clearTimeout(timer);timer=null;
 $('#refresh').disabled=true;$('#refresh').textContent='분석 중…';$('#error').textContent='';render();
 const results=await Promise.allSettled([
  Scan('~').then(result=>{items=new Map(result.items.map(i=>[i.id,i]));scanValid=true;}),
  ScanStorage().then(drawStorage),
 ]);
 busy=false;clearTimeout(timer);timer=null;render();
 $('#refresh').disabled=false;$('#refresh').textContent='다시 분석';$('#progress').textContent='';
 const failures=results.filter(r=>r.status==='rejected');
 $('#error').textContent=failures.map(r=>String(r.reason)).join(' · ');
}
EventsOn('scan:progress',p=>{
 if(!busy)return;
 $('#progress').textContent=p.phase==='discover'?'파일 탐색 중…':`정리 후보 ${items.size}개 발견`;
 if(p.item){items.set(p.item.id,p.item);schedule();}
});
EventsOn('storage:progress',value=>{if(busy)drawStorage(value);});
$('#review').addEventListener('cancel',event=>{if($('#cancel')?.disabled)event.preventDefault();});
$('#refresh').onclick=refresh;
StorageInfo().then(drawStorage).catch(err=>{$('#error').textContent=String(err);});
