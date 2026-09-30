import './style.css';
import { ExecuteCleanup, PreviewCleanup, Scan, SelectDirectory } from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';

const app = document.querySelector('#app');
const formatBytes = (bytes) => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = Number(bytes || 0); let index = 0;
  while (value >= 1024 && index < units.length - 1) { value /= 1024; index += 1; }
  return `${value.toFixed(value >= 100 ? 0 : value >= 10 ? 1 : 2)} ${units[index]}`;
};
const escapeHtml = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char]));
const tierIcon = { safe: '🟢', caution: '🟡', review: '🟠', protected: '🔴' };

app.innerHTML = `
  <header><div class="brand"><div class="mark">S</div><div><h1>Shed</h1><p>Reclaim gigabytes. Lose nothing you can't rebuild.</p></div></div><span class="local">Native Wails · local only</span></header>
  <main>
    <section class="hero"><div><h2>Disk health</h2><p>Find rebuildable data, large personal files, and stale items before deciding what to remove.</p></div><div class="scanbar"><input id="root" value="~" aria-label="Scan folder" /><button id="browse" class="secondary">Choose folder</button><button id="scan">Scan</button></div></section>
    <div id="progress" class="progress">Ready</div>
    <section id="summary" class="cards"><div class="card"><small>Status</small><strong>Ready</strong><small>No files changed</small></div></section>
    <section class="panel"><div class="panel-head"><div><h3>Reclaim candidates</h3><small>Items are grouped by cleanup reason so each action keeps its context.</small></div><div class="filters"><select id="tier"><option value="">All tiers</option><option>safe</option><option>caution</option><option>review</option><option>protected</option></select><input id="filter" placeholder="Filter path or rule" /></div></div><div class="actionbar"><label><input type="checkbox" id="risk" /> Allow caution and review items</label><div class="action-right"><strong id="selection">0 selected</strong><select id="mode"><option value="delete">Delete now</option><option value="quarantine">Move to quarantine</option></select><button id="preview" disabled>Review cleanup</button></div></div><div id="table" class="empty">Choose a folder and start a scan.</div></section>
    <section id="review" class="panel review-panel hidden"></section>
    <p class="note">Protected paths can never be selected. Every cleanup is revalidated immediately before execution. Personal files are review candidates, never automatic safe recommendations.</p>
  </main>`;

let items = [];
let groups = [];
let selected = new Set();

function selectedItems() { return items.filter((item) => selected.has(item.id)); }
function draw(result) {
  items = result.items || [];
  groups = result.groups || [];
  selected.clear();
  const summary = result.summary || {};
  document.querySelector('#summary').innerHTML = ['safe', 'caution', 'review', 'protected'].map((tier) => `<div class="card"><small>${tier[0].toUpperCase() + tier.slice(1)}</small><strong class="${tier}">${formatBytes(summary[tier]?.bytes)}</strong><small>${summary[tier]?.count || 0} items</small></div>`).join('');
  document.querySelector('#progress').textContent = `Finished in ${result.durationMs || 0} ms · ${items.length} candidates`;
  document.querySelector('#review').classList.add('hidden');
  renderTable(); updateSelection();
}
function isSelectable(item) {
  if (item.tier === 'safe') return true;
  return (item.tier === 'caution' || item.tier === 'review') && document.querySelector('#risk').checked;
}
function visibleItems(group) {
  const tier = document.querySelector('#tier').value;
  const query = document.querySelector('#filter').value.toLowerCase();
  return group.itemIds.map((id) => items.find((item) => item.id === id)).filter((item) => item && (!tier || item.tier === tier) && (!query || `${item.path} ${item.ruleId} ${item.name}`.toLowerCase().includes(query)));
}
function renderTable() {
  const visibleGroups = groups.map((group) => ({ group, items: visibleItems(group) })).filter(({ items: groupItems }) => groupItems.length);
  document.querySelector('#table').innerHTML = visibleGroups.length ? `<div class="table-groups">${visibleGroups.map(({ group, items: groupItems }) => {
    const canSelect = groupItems.some(isSelectable);
    return `<section class="table-group"><div class="table-group-head"><div><span class="badge ${group.tier}">${tierIcon[group.tier]} ${group.tier}</span><h4>${escapeHtml(group.title)}</h4><p>${escapeHtml(group.description)}</p></div><div class="group-actions"><strong>${formatBytes(groupItems.reduce((sum, item) => sum + item.bytes, 0))}</strong><small>${groupItems.length} item(s)</small><button class="secondary" data-group="${escapeHtml(group.id)}" ${canSelect ? '' : 'disabled'}>Select group</button></div></div><table><thead><tr><th>Select</th><th>Item</th><th>Tier</th><th>Size</th><th>Why this is here</th></tr></thead><tbody>${groupItems.map((item) => `<tr><td><input type="checkbox" data-id="${escapeHtml(item.id)}" ${selected.has(item.id) ? 'checked' : ''} ${isSelectable(item) ? '' : 'disabled'} /></td><td><strong>${escapeHtml(item.name)}</strong><small class="path">${escapeHtml(item.path)}</small></td><td><span class="badge ${item.tier}">${tierIcon[item.tier]} ${item.tier}</span></td><td>${formatBytes(item.bytes)}<small>${escapeHtml(item.cost)} rebuild cost</small></td><td class="details">${escapeHtml(item.explanation)}<small>Rebuild or next step: ${escapeHtml(item.rebuild)}</small>${item.native ? `<small>Native guide: ${escapeHtml(item.native)}</small>` : ''}${item.signals?.length ? `<small>Signals: ${escapeHtml(item.signals.join(' · '))}</small>` : ''}</td></tr>`).join('')}</tbody></table></section>`;
  }).join('')}</div>` : '<div class="empty">No matching candidates. Try another folder or filter.</div>';
  document.querySelectorAll('[data-id]').forEach((checkbox) => checkbox.onchange = () => { checkbox.checked ? selected.add(checkbox.dataset.id) : selected.delete(checkbox.dataset.id); updateSelection(); });
  document.querySelectorAll('[data-group]').forEach((button) => button.onclick = () => {
    const group = groups.find((candidate) => candidate.id === button.dataset.group);
    if (!group) return;
    group.itemIds.forEach((id) => { const item = items.find((candidate) => candidate.id === id); if (item && isSelectable(item)) selected.add(id); });
    renderTable(); updateSelection();
  });
}
function updateSelection() {
  const chosen = selectedItems();
  document.querySelector('#selection').textContent = `${chosen.length} selected · ${formatBytes(chosen.reduce((sum, item) => sum + item.bytes, 0))}`;
  document.querySelector('#preview').disabled = chosen.length === 0;
}
function requestPayload(confirmation = '') {
  return { ids: [...selected], mode: document.querySelector('#mode').value, acknowledgeRisk: document.querySelector('#risk').checked, confirmation };
}
function showPreview(preview) {
  const review = document.querySelector('#review'); review.classList.remove('hidden');
  review.innerHTML = `<div class="panel-head"><div><h3>Final cleanup review</h3><small>${preview.mode === 'delete' ? 'Deletion immediately reclaims disk space and cannot be undone.' : 'Quarantine is recoverable but usually does not reclaim space on the same volume.'}</small></div><strong>${formatBytes(preview.totalBytes)}</strong></div><div class="review-body"><div class="review-list">${preview.items.map((item) => `<div><span class="badge ${item.tier}">${tierIcon[item.tier]} ${item.tier}</span><strong>${escapeHtml(item.name)}</strong><small>${escapeHtml(item.path)}</small></div>`).join('')}</div>${preview.blocked.length ? `<div class="blocked"><strong>${preview.blocked.length} blocked</strong>${preview.blocked.map((item) => `<small>${escapeHtml(item.path || item.id)} — ${escapeHtml(item.reason)}</small>`).join('')}</div>` : ''}<div class="confirm"><label>Type <code>${escapeHtml(preview.confirmationToken)}</code> to continue<input id="confirm-token" autocomplete="off" /></label><button id="execute" class="danger" disabled>${preview.mode === 'delete' ? 'Delete selected items' : 'Move to quarantine'}</button></div></div>`;
  const input = document.querySelector('#confirm-token'); const execute = document.querySelector('#execute');
  input.oninput = () => { execute.disabled = input.value !== preview.confirmationToken; };
  execute.onclick = async () => {
    execute.disabled = true; execute.textContent = 'Working…';
    try {
      const result = await ExecuteCleanup(requestPayload(input.value));
      review.innerHTML = `<div class="result"><h3>Cleanup complete</h3><strong>${formatBytes(result.estimatedBytes)} processed</strong><p>${formatBytes(result.actualBytes)} measured as newly available disk space.</p><p>${result.completed.length} completed · ${result.failed.length} failed</p><button id="rescan">Rescan</button></div>`;
      document.querySelector('#rescan').onclick = runScan;
    } catch (error) { execute.disabled = false; execute.textContent = 'Try again'; alert(error?.message || String(error)); }
  };
  review.scrollIntoView({ behavior: 'smooth', block: 'start' });
}
async function chooseDirectory() {
  try {
    const directory = await SelectDirectory();
    if (directory) document.querySelector('#root').value = directory;
  } catch (error) { alert(error?.message || String(error)); }
}
async function runScan() {
  const button = document.querySelector('#scan'); button.disabled = true; button.textContent = 'Scanning…';
  try { draw(await Scan(document.querySelector('#root').value || '~')); }
  catch (error) { document.querySelector('#table').textContent = error?.message || String(error); }
  finally { button.disabled = false; button.textContent = 'Scan again'; }
}

document.querySelector('#browse').onclick = chooseDirectory;
document.querySelector('#scan').onclick = runScan;
document.querySelector('#tier').onchange = renderTable;
document.querySelector('#filter').oninput = renderTable;
document.querySelector('#risk').onchange = () => {
  if (!document.querySelector('#risk').checked) selectedItems().filter((item) => item.tier !== 'safe').forEach((item) => selected.delete(item.id));
  renderTable(); updateSelection();
};
document.querySelector('#preview').onclick = async () => {
  try { showPreview(await PreviewCleanup(requestPayload())); }
  catch (error) { alert(error?.message || String(error)); }
};
EventsOn('scan:progress', (progress) => {
  const suffix = progress.path ? ` · ${progress.path}` : '';
  document.querySelector('#progress').textContent = `${progress.phase}: ${progress.scanned} · ${progress.candidates} candidates${suffix}`;
});
