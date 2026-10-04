import React from 'react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { analyze, folderMap, onScanProgress, revealPath, storageInfo } from './lib/api';
import { layoutTreemap } from './lib/treemap';

const DAY = 86_400_000;

function bytes(value) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let amount = Number(value || 0);
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) { amount /= 1024; unit += 1; }
  return `${amount.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function ageClass(value) {
  const age = value ? (Date.now() - Date.parse(value)) / DAY : Infinity;
  if (age <= 7) return 'age-now';
  if (age <= 30) return 'age-month';
  if (age <= 180) return 'age-half';
  if (age <= 365) return 'age-year';
  return 'age-old';
}

function modifiedLabel(value) {
  if (!value) return '변경일 알 수 없음';
  const days = Math.max(0, Math.floor((Date.now() - Date.parse(value)) / DAY));
  if (days === 0) return '오늘 변경';
  if (days < 30) return `${days}일 전 변경`;
  if (days < 365) return `${Math.floor(days / 30)}개월 전 변경`;
  return `${Math.floor(days / 365)}년 전 변경`;
}

function pathParts(path) {
  if (!path || path === '/') return [{ label: '/', path: '/' }];
  const parts = path.split('/').filter(Boolean);
  return [{ label: '/', path: '/' }, ...parts.map((label, index) => ({ label, path: `/${parts.slice(0, index + 1).join('/')}` }))];
}

const MIN_TILE_AREA_PX = 4500;
const MIN_TILE_WIDTH_PX = 100;
const MIN_TILE_HEIGHT_PX = 56;
const VIRTUAL_LAYOUT_SCALE = 0.35;
const MAX_VIRTUAL_AREA_SHARE = 0.03;

// A virtual directory keeps the map readable without changing the real byte
// proportions. It is based on the same rendered geometry used by the map,
// so the group contains only cells that would otherwise be hard to see.
function isUnreadableCell(cell, viewport) {
  const width = viewport.width || 1100;
  const height = viewport.height || 600;
  const pixelWidth = cell.width / 100 * width;
  const pixelHeight = cell.height / 100 * height;
  return pixelWidth * pixelHeight < MIN_TILE_AREA_PX || pixelWidth < MIN_TILE_WIDTH_PX || pixelHeight < MIN_TILE_HEIGHT_PX;
}

function cellDensity(cell, viewport) {
  const width = cell.width / 100 * (viewport.width || 1100);
  const height = cell.height / 100 * (viewport.height || 600);
  if (width >= 116 && height >= 74) return 3;
  if (width >= 72 && height >= 42) return 2;
  if (width >= 40 && height >= 22) return 1;
  return 0;
}

function createVirtualGroup(rows, direct, virtualDepth) {
  const sortedSmall = [...rows].sort((a, b) => Number(b.bytes || 0) - Number(a.bytes || 0));
  const representative = sortedSmall[0];
  const groupedBytes = sortedSmall.reduce((sum, row) => sum + Number(row.bytes || 0), 0);
  const directBytes = direct.reduce((sum, row) => sum + Number(row.bytes || 0), 0);
  const scaledBytes = groupedBytes * VIRTUAL_LAYOUT_SCALE;
  const maxVisualBytes = directBytes > 0
    ? directBytes * MAX_VIRTUAL_AREA_SHARE / (1 - MAX_VIRTUAL_AREA_SHARE)
    : scaledBytes;
  const virtual = {
    name: `${representative.name || '항목'}${sortedSmall.length > 1 ? ` 외 ${sortedSmall.length - 1}개` : ''}`,
    path: `virtual:${representative.path || 'items'}`,
    bytes: groupedBytes,
    layoutBytes: Math.min(scaledBytes, maxVisualBytes),
    files: sortedSmall.reduce((sum, row) => sum + Number(row.files || 1), 0),
    modifiedAt: sortedSmall.reduce((latest, row) => (!latest || Date.parse(row.modifiedAt || '') > Date.parse(latest)) ? row.modifiedAt : latest, ''),
    directory: true,
    virtual: true,
    virtualDepth: virtualDepth + 1,
    groupedCount: sortedSmall.length,
    children: sortedSmall,
  };
  return virtual;
}

function groupSmallChildren(children, viewport = { width: 0, height: 0 }, virtualDepth = 0) {
  if (children.length < 2) return children;
  let direct = [...children].sort((a, b) => Number(b.bytes || 0) - Number(a.bytes || 0));
  let grouped = [];

  for (let pass = 0; pass < children.length; pass += 1) {
    const virtual = grouped.length ? createVirtualGroup(grouped, direct, virtualDepth) : null;
    const display = virtual ? [...direct, virtual] : direct;
    const unreadablePaths = new Set(layoutTreemap(display, viewport)
      .filter(cell => !cell.virtual && isUnreadableCell(cell, viewport))
      .map(cell => cell.path));
    if (!unreadablePaths.size) return display;

    let move = direct.filter(child => unreadablePaths.has(child.path));
    if (direct.length - move.length < 1) {
      const keep = new Set(direct.slice(0, 1).map(child => child.path));
      move = direct.filter(child => !keep.has(child.path));
    }
    if (!move.length) return display;
    grouped = [...grouped, ...move];
    const moved = new Set(move.map(child => child.path));
    direct = direct.filter(child => !moved.has(child.path));
  }

  return grouped.length ? [...direct, createVirtualGroup(grouped, direct, virtualDepth)] : direct;
}

function Treemap({ data, selected, onSelect, onOpen, onHover, onPrefetch }) {
  const mapRef = useRef(null);
  const [viewport, setViewport] = useState({ width: 0, height: 0 });
  useEffect(() => {
    const node = mapRef.current;
    if (!node) return undefined;
    const update = entry => {
      const width = Math.round(entry?.contentRect?.width || node.getBoundingClientRect().width);
      const height = Math.round(entry?.contentRect?.height || node.getBoundingClientRect().height);
      setViewport(previous => previous.width === width && previous.height === height ? previous : { width, height });
    };
    update();
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(entries => { if (entries[0]) update(entries[0]); });
    observer?.observe(node);
    window.addEventListener('resize', update);
    return () => { observer?.disconnect(); window.removeEventListener('resize', update); };
  }, [data]);
  const displayChildren = useMemo(() => groupSmallChildren(data?.children || [], viewport, data?.virtualDepth || 0), [data, viewport]);
  const cells = useMemo(() => layoutTreemap(displayChildren, viewport), [displayChildren, viewport]);
  if (!cells.length) return <div className="map-empty">이 폴더에는 표시할 파일이 없습니다.</div>;
  return (
    <div ref={mapRef} className="treemap" role="tree" aria-label={`${data.path} 용량 지도`}>
      {cells.map(cell => {
        const density = Math.max(cell.virtual ? 1 : 0, cellDensity(cell, viewport));
        return (
          <button
            type="button"
            role="treeitem"
            key={cell.path}
            className={`map-cell density-${density} ${ageClass(cell.modifiedAt)} ${selected?.path === cell.path ? 'selected' : ''}`}
            style={{ left: `${cell.x}%`, top: `${cell.y}%`, width: `${cell.width}%`, height: `${cell.height}%` }}
            onClick={() => onSelect(cell)}
            onDoubleClick={() => cell.directory && onOpen(cell)}
            onMouseEnter={() => { onHover(cell); if (cell.directory && !cell.virtual) onPrefetch(cell.path); }}
            onMouseLeave={() => onHover(null)}
            onFocus={() => onHover(cell)}
            onBlur={() => onHover(null)}
            title={`${cell.virtual ? cell.name : cell.path}\n${bytes(cell.bytes)} · ${modifiedLabel(cell.modifiedAt)}`}
          >
            {density >= 1 && <span className="cell-name">{cell.name}</span>}
            {density >= 2 && <span className="cell-size">{bytes(cell.bytes)}{cell.virtual && cell.groupedCount > 1 ? ` · ${cell.groupedCount}개` : ''}</span>}
            {density >= 3 && <span className="cell-date">{modifiedLabel(cell.modifiedAt)}</span>}
          </button>
        );
      })}
    </div>
  );
}

export default function App() {
  const mapCacheRef = useRef(new Map());
  const prefetchingRef = useRef(new Set());
  const [data, setData] = useState(null);
  const [storage, setStorage] = useState(null);
  const [history, setHistory] = useState([]);
  const [selected, setSelected] = useState(null);
  const [loading, setLoading] = useState(true);
  const [scanning, setScanning] = useState(false);
  const [progress, setProgress] = useState(null);
  const [hovered, setHovered] = useState(null);
  const [virtualStack, setVirtualStack] = useState([]);
  const [error, setError] = useState('');

  const loadMap = async (path = '', remember = false) => {
    const cached = mapCacheRef.current.get(path);
    if (cached) {
      if (remember && data) setHistory(stack => [...stack, data.path]);
      setData(cached);
      setVirtualStack([]);
      setSelected(null);
      setHovered(null);
      setLoading(false);
      return;
    }
    setLoading(true);
    setError('');
    try {
      const next = await folderMap(path);
      mapCacheRef.current.set(path, next);
      mapCacheRef.current.set(next.path, next);
      if (remember && data) setHistory(stack => [...stack, data.path]);
      setData(next);
      setVirtualStack([]);
      setSelected(null);
      setHovered(null);
    } catch (reason) {
      setError(String(reason));
    } finally {
      setLoading(false);
    }
  };

  const prefetchMap = path => {
    if (mapCacheRef.current.has(path) || prefetchingRef.current.has(path)) return;
    prefetchingRef.current.add(path);
    folderMap(path)
      .then(next => {
        mapCacheRef.current.set(path, next);
        mapCacheRef.current.set(next.path, next);
      })
      .catch(() => {})
      .finally(() => prefetchingRef.current.delete(path));
  };

  useEffect(() => {
    storageInfo().then(setStorage).catch(() => {});
    loadMap();
    const cancel = onScanProgress(setProgress);
    return typeof cancel === 'function' ? cancel : undefined;
  }, []);

  const startAnalysis = async () => {
    setScanning(true);
    setProgress({ phase: 'discover', filesScanned: 0 });
    setError('');
    mapCacheRef.current.clear();
    prefetchingRef.current.clear();
    try {
      await analyze();
      await Promise.all([loadMap(data?.path || ''), storageInfo().then(setStorage)]);
    } catch (reason) {
      setError(String(reason));
    } finally {
      setScanning(false);
      setProgress(null);
    }
  };

  const goBack = async () => {
    if (virtualStack.length) {
      setVirtualStack(stack => stack.slice(0, -1));
      setSelected(null);
      setHovered(null);
      return;
    }
    const target = history.at(-1);
    if (target == null) return;
    setHistory(stack => stack.slice(0, -1));
    await loadMap(target);
  };

  const openCell = cell => {
    if (cell.virtual) {
      setVirtualStack(stack => [...stack, { group: cell }]);
      setSelected(null);
      setHovered(null);
      return;
    }
    if (cell.directory) loadMap(cell.path, true);
  };

  const jumpTo = async path => {
    if (path === data?.path) return;
    const currentParts = pathParts(data?.path).map(part => part.path);
    const index = currentParts.indexOf(path);
    if (index >= 0) {
      setHistory(currentParts.slice(0, index));
      setVirtualStack([]);
    }
    await loadMap(path);
  };

  const used = storage ? Math.max(0, Number(storage.total) - Number(storage.available)) : 0;
  const scanCount = Number(progress?.filesScanned || progress?.scanned || 0);
  const viewData = virtualStack.length ? virtualStack[virtualStack.length - 1].group : data;
  const breadcrumbItems = data ? [
    ...pathParts(data.path),
    ...virtualStack.map((entry, index) => ({ label: entry.group.name, virtualIndex: index, path: `virtual:${index}` })),
  ] : [];

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="brand">Shed</div>
        <nav className="breadcrumbs" aria-label="현재 경로">
          <button className="back" onClick={goBack} disabled={!history.length} aria-label="뒤로">‹</button>
          {breadcrumbItems.map((part, index, all) => (
            <span key={part.path}>
              <button onClick={() => part.virtualIndex == null ? jumpTo(part.path) : setVirtualStack(stack => stack.slice(0, part.virtualIndex + 1))} disabled={index === all.length - 1}>{part.label}</button>
              {index > 0 && index < all.length - 1 && <i>/</i>}
            </span>
          ))}
        </nav>
        <div className="disk-summary">
          {storage && <span>{bytes(storage.available)} 여유 <small>{bytes(used)} 사용</small></span>}
          <button className="scan-button" onClick={startAnalysis} disabled={scanning}>{scanning ? '분석 중' : '분석'}</button>
        </div>
      </header>

      <main className="workspace">
        <section className="map-header">
          <div>
            <h1>{viewData?.name || '디스크 지도'}</h1>
            <p>{viewData ? `${bytes(viewData.bytes)} · ${viewData.files.toLocaleString()}개 파일` : '용량을 면적으로, 최근 변경 시점을 색으로 표시합니다.'}</p>
          </div>
          <div className="legend" aria-label="색상 범례">
            <span className="age-now">7일</span><span className="age-month">30일</span><span className="age-half">6개월</span><span className="age-year">1년</span><span className="age-old">오래됨</span>
          </div>
        </section>

        <section className={`map-stage ${loading ? 'loading' : ''}`}>
          {viewData && <Treemap data={viewData} selected={selected} onSelect={setSelected} onHover={setHovered} onPrefetch={prefetchMap} onOpen={openCell} />}
          {hovered && <div className="map-hover-card" role="status"><strong>{hovered.name}</strong><span>{bytes(hovered.bytes)} · {modifiedLabel(hovered.modifiedAt)}</span><small>{hovered.virtual ? `${hovered.groupedCount}개 합산 · 가상 폴더` : hovered.path}</small>{hovered.directory && <em>더블 클릭하여 열기</em>}</div>}
          {!data && !loading && <div className="first-run"><strong>디스크 지도를 만들 준비가 됐습니다.</strong><span>분석하면 큰 폴더부터 지도에 나타납니다.</span><button onClick={startAnalysis}>분석 시작</button></div>}
          {loading && <div className="map-loading">지도 불러오는 중…</div>}
        </section>

        <footer className="inspector">
          <div className="scan-status">
            {scanning ? <><span className="pulse" /> 파일 탐색 중 · {scanCount.toLocaleString()}개 확인</> : viewData ? <>{viewData.children.length.toLocaleString()}개 항목 · {data.path}</> : null}
          </div>
          {selected && <div className="selection"><strong>{selected.virtual ? selected.name : selected.path}</strong><span>{bytes(selected.bytes)} · {modifiedLabel(selected.modifiedAt)}</span>{selected.directory && <button onClick={() => openCell(selected)}>열기</button>}{!selected.virtual && <button onClick={() => revealPath(selected.path)}>Finder</button>}</div>}
        </footer>
        {error && <div className="error" role="alert">{error}</div>}
      </main>
    </div>
  );
}
