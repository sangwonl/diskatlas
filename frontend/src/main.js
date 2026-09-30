import './style.css';
import { Scan } from '../wailsjs/go/main/App';

const app = document.querySelector('#app');
const formatBytes = (bytes) => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = Number(bytes || 0); let index = 0;
  while (value >= 1024 && index < units.length - 1) { value /= 1024; index += 1; }
  return `${value.toFixed(value >= 100 ? 0 : value >= 10 ? 1 : 2)} ${units[index]}`;
};
const escapeHtml = (value) => String(value ?? '').replace(/[&<>\"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '\"': '&quot;', "'": '&#39;' }[char]));

app.innerHTML = `
  <header><div class="brand"><div class="mark">S</div><div><h1>Shed</h1><p>Reclaim gigabytes. Lose nothing you can't rebuild.</p></div></div><span class="local">Native Wails · local only</span></header>
  <main>
    <section class="hero"><div><h2>Developer disk health</h2><p>Read-only scan powered by the Go core.</p></div><div class="scanbar"><input id="root" value="~" aria-label="Scan root" /><button id="scan">Scan</button></div></section>
    <section id="summary" class="cards"><div class="card"><small>Status</small><strong>Ready</strong><small>No files changed</small></div></section>
    <section class="panel"><div class="panel-head"><h3>Reclaim candidates</h3><div class="filters"><select id="tier"><option value="">All tiers</option><option>safe</option><option>caution</option><option>review</option><option>protected</option></select><input id="filter" placeholder="Filter path or rule" /></div></div><div id="table" class="empty">Start a scan to see explainable candidates.</div></section>
    <p class="note">Protected paths remain visible for context and cannot be deleted. This native shell exposes no deletion or quarantine API.</p>
  </main>`;

let items = [];
const tierIcon = { safe: '🟢', caution: '🟡', review: '🟠', protected: '🔴' };
function draw(result) {
  items = result.items || [];
  const summary = result.summary || {};
  document.querySelector('#summary').innerHTML = ['safe', 'caution', 'review', 'protected'].map((tier) => `<div class="card"><small>${tier[0].toUpperCase() + tier.slice(1)}</small><strong class="${tier}">${formatBytes(summary[tier]?.bytes)}</strong><small>${summary[tier]?.count || 0} items</small></div>`).join('');
  renderTable();
}
function renderTable() {
  const tier = document.querySelector('#tier').value;
  const query = document.querySelector('#filter').value.toLowerCase();
  const rows = items.filter((item) => (!tier || item.tier === tier) && (!query || `${item.path} ${item.ruleId} ${item.name}`.toLowerCase().includes(query)));
  document.querySelector('#table').innerHTML = rows.length ? `<table><thead><tr><th>Item</th><th>Tier</th><th>Size</th><th>Why</th></tr></thead><tbody>${rows.map((item) => `<tr><td><strong>${escapeHtml(item.name)}</strong><small>${escapeHtml(item.path)}</small></td><td><span class="badge ${item.tier}">${tierIcon[item.tier]} ${item.tier}</span></td><td>${formatBytes(item.bytes)}<small>${escapeHtml(item.cost)} rebuild cost</small></td><td>${escapeHtml(item.explanation)}<small>Rebuild: ${escapeHtml(item.rebuild)}</small></td></tr>`).join('')}</tbody></table>` : '<div class="empty">No matching items.</div>';
}
document.querySelector('#scan').onclick = async () => {
  const button = document.querySelector('#scan'); button.disabled = true; button.textContent = 'Scanning…';
  try { const result = await Scan(document.querySelector('#root').value || '~'); draw(result); }
  catch (error) { document.querySelector('#table').textContent = error?.message || String(error); }
  finally { button.disabled = false; button.textContent = 'Scan again'; }
};
document.querySelector('#tier').onchange = renderTable;
document.querySelector('#filter').oninput = renderTable;
