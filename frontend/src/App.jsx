import React from 'react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { chooseScanRoot, folderMap, getScanRoot, measureFolderMap, onScanProgress, refreshFolderMap, revealPath, storageInfo } from './lib/api';
import { layoutTreemap } from './lib/treemap';

const DAY = 86_400_000;

function hasMeasuredFolderMap(map) {
  const generatedAt = Date.parse(map?.generatedAt || '');
  return Number.isFinite(generatedAt);
}

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

function sizeLabel(item) {
  if (item.sizeKnown === false) return '용량 미확인';
  if (item.sizeStale) return `≈ ${bytes(item.bytes)}`;
  return `${item.sizeComplete === false ? '≥ ' : ''}${bytes(item.bytes)}`;
}

function pathParts(path) {
  if (!path) return [];
  const normalized = path.replace(/\\/g, '/');
  const drive = normalized.match(/^([A-Za-z]:)(?:\/|$)/)?.[1];
  const unc = normalized.startsWith('//');
  const separator = path.includes('\\') ? '\\' : '/';
  let rootPath = '/';
  let rootLabel = '/';
  let remainder = normalized.replace(/^\/+/, '');
  if (drive) {
    rootPath = `${drive}${separator}`;
    rootLabel = drive;
    remainder = normalized.slice(3);
  } else if (unc) {
    const segments = normalized.slice(2).split('/').filter(Boolean);
    const server = segments.shift();
    const share = segments.shift();
    rootLabel = `\\\\${server || ''}${share ? `\\${share}` : ''}`;
    rootPath = rootLabel;
    remainder = segments.join('/');
  }
  const parts = remainder.split('/').filter(Boolean);
  return [
    { label: rootLabel, path: rootPath },
    ...parts.map((label, index) => ({ label, path: `${rootPath.replace(/[\\/]$/, '')}${separator}${parts.slice(0, index + 1).join(separator)}` })),
  ];
}

function parentPath(path) {
  if (!path) return '';
  const normalized = path.replace(/\\/g, '/').replace(/\/$/, '');
  const drive = normalized.match(/^([A-Za-z]:)(?:\/|$)/)?.[1];
  const separator = path.includes('\\') ? '\\' : '/';
  if (!normalized || normalized === '/') return '';
  if (drive && normalized.length <= 3) return '';
  const index = normalized.lastIndexOf('/');
  if (index < 0) return '';
  let parent = normalized.slice(0, index);
  if (drive && index <= 2) parent = `${drive}/`;
  if (!parent) parent = '/';
  return parent.replace(/\//g, separator);
}

function pathKey(path) {
  const normalized = String(path || '').replace(/\\/g, '/').replace(/\/$/, '') || '/';
  return /^[A-Za-z]:/.test(normalized) ? normalized.toLowerCase() : normalized;
}

function scopedPathParts(path, scopeRoot) {
  const parts = pathParts(path);
  if (!scopeRoot) return parts;
  const rootKey = pathKey(scopeRoot);
  const rootIndex = parts.findIndex(part => pathKey(part.path) === rootKey);
  return rootIndex < 0 ? parts : parts.slice(rootIndex);
}

function layoutEntries(data) {
  const children = data?.children || [];
  return children
    .filter(child => child.sizeKnown !== false && Number(child.bytes || 0) > 0)
    .map(child => ({ ...child, layoutBytes: Number(child.bytes || 0) }));
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
  const groupedLayoutBytes = sortedSmall.reduce((sum, row) => sum + Number(row.layoutBytes ?? row.bytes ?? 0), 0);
  const directLayoutBytes = direct.reduce((sum, row) => sum + Number(row.layoutBytes ?? row.bytes ?? 0), 0);
  const scaledBytes = groupedLayoutBytes * VIRTUAL_LAYOUT_SCALE;
  const maxVisualBytes = directLayoutBytes > 0
    ? directLayoutBytes * MAX_VIRTUAL_AREA_SHARE / (1 - MAX_VIRTUAL_AREA_SHARE)
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
    sizeKnown: sortedSmall.every(row => row.sizeKnown !== false),
    sizeComplete: sortedSmall.every(row => row.sizeComplete !== false),
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
  const displayChildren = useMemo(() => groupSmallChildren(layoutEntries(data), viewport, data?.virtualDepth || 0), [data, viewport]);
  const cells = useMemo(() => layoutTreemap(displayChildren, viewport), [displayChildren, viewport]);
  if (!cells.length) return null;
  return (
    <div ref={mapRef} className="treemap" role="tree" aria-label={`${data.path} 용량 지도`}>
      {cells.map(cell => {
        const density = Math.max(cell.virtual ? 1 : 0, cellDensity(cell, viewport));
        return (
          <button
            type="button"
            role="treeitem"
            key={cell.path}
            className={`map-cell density-${density} ${cell.sizeKnown === false ? 'size-unknown' : ageClass(cell.modifiedAt)} ${selected?.path === cell.path ? 'selected' : ''}`}
            style={{ left: `${cell.x}%`, top: `${cell.y}%`, width: `${cell.width}%`, height: `${cell.height}%` }}
            onClick={() => onSelect(cell)}
            onDoubleClick={() => cell.directory && onOpen(cell)}
            onMouseEnter={() => { onHover(cell); if (cell.directory && !cell.virtual) onPrefetch(cell.path); }}
            onMouseLeave={() => onHover(null)}
            onFocus={() => onHover(cell)}
            onBlur={() => onHover(null)}
            title={`${cell.virtual ? cell.name : cell.path}\n${sizeLabel(cell)}${cell.sizeStale ? ' · 저장된 측정값' : cell.sizeComplete === false ? ' · 하위 일부 용량 제외' : ''} · ${modifiedLabel(cell.modifiedAt)}`}
          >
            {density >= 1 && <span className="cell-name">{cell.name}</span>}
            {density >= 2 && <span className="cell-size">{`${sizeLabel(cell)}${cell.virtual && cell.groupedCount > 1 ? ` · ${cell.groupedCount}개` : ''}`}</span>}
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
  const currentPathRef = useRef(null);
  const loadRequestRef = useRef(0);
  const activeProgressRequestRef = useRef('');
  const progressCountRef = useRef(0);
  const initializedRef = useRef(false);
  const [data, setData] = useState(null);
  const [storage, setStorage] = useState(null);
  const [scanRoot, setScanRoot] = useState('');
  const [history, setHistory] = useState([]);
  const [selected, setSelected] = useState(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [progress, setProgress] = useState(null);
  const [hovered, setHovered] = useState(null);
  const [virtualStack, setVirtualStack] = useState([]);
  const [error, setError] = useState('');
  const [choosingRoot, setChoosingRoot] = useState(false);

  const acceptMap = next => {
    currentPathRef.current = next.path;
    setData(next);
    setLoading(false);
  };

  const updateParentCache = next => {
    let childMap = next;
    let path = parentPath(childMap.path);
    while (path) {
      const parent = mapCacheRef.current.get(path);
      if (!parent) return;
      const children = parent.children.map(child => child.path === childMap.path
        ? { ...child, bytes: childMap.bytes, files: childMap.files, modifiedAt: childMap.modifiedAt, sizeKnown: childMap.sizeKnown, sizeComplete: childMap.sizeComplete, sizeStale: false }
        : child);
      const measured = children.every(child => child.sizeComplete === true);
      childMap = {
        ...parent,
        children,
        bytes: children.reduce((sum, child) => sum + Number(child.bytes || 0), 0),
        files: children.reduce((sum, child) => sum + Number(child.files || 0), 0),
        measured,
        sizeKnown: true,
        sizeComplete: measured,
        modifiedAt: children.reduce((latest, child) => !latest || Date.parse(child.modifiedAt || '') > Date.parse(latest) ? child.modifiedAt : latest, ''),
      };
      mapCacheRef.current.set(parent.path, childMap);
      path = parentPath(childMap.path);
    }
  };

  const reloadCurrentMap = async () => {
    if (!data || loading || refreshing || virtualStack.length) return;
    const request = ++loadRequestRef.current;
    const progressRequestID = String(request);
    activeProgressRequestRef.current = progressRequestID;
    progressCountRef.current = 0;
    setRefreshing(true);
    setScanning(true);
    setProgress({ phase: 'folder-map', requestID: progressRequestID, filesScanned: 0, path: data.path });
    setError('');
    try {
      const next = await refreshFolderMap(data.path, progressRequestID);
      if (request !== loadRequestRef.current) return;
      mapCacheRef.current.set(next.path, next);
      updateParentCache(next);
      acceptMap(next);
      setSelected(null);
      setHovered(null);
      storageInfo().then(setStorage).catch(() => {});
    } catch (reason) {
      if (request === loadRequestRef.current) setError(String(reason));
    } finally {
      if (request === loadRequestRef.current) {
        activeProgressRequestRef.current = '';
        setRefreshing(false);
        setScanning(false);
        setProgress(null);
      }
    }
  };

  const loadMap = async (path = '', remember = false, force = false, measure = false) => {
    const key = path || data?.root || currentPathRef.current || scanRoot || '/';
    const cached = mapCacheRef.current.get(key);
    if (cached && !force && hasMeasuredFolderMap(cached) && (!measure || cached.sizeKnown !== false)) {
      loadRequestRef.current += 1;
      activeProgressRequestRef.current = '';
      if (remember && data) setHistory(stack => [...stack, data.path]);
      acceptMap(cached);
      setScanning(false);
      setProgress(null);
      setVirtualStack([]);
      setSelected(null);
      setHovered(null);
      return cached;
    }
    const request = ++loadRequestRef.current;
    const progressRequestID = measure ? String(request) : '';
    activeProgressRequestRef.current = progressRequestID;
    progressCountRef.current = 0;
    const previousPath = data?.path;
    if (remember && previousPath) setHistory(stack => [...stack, previousPath]);
    setLoading(true);
    setRefreshing(false);
    setScanning(measure && force);
    setProgress(measure && force ? { phase: 'folder-map', requestID: progressRequestID, filesScanned: 0, path } : null);
    setError('');
    try {
      let next;
      if (measure && force) {
        next = await refreshFolderMap(path, progressRequestID);
      } else if (measure) {
        next = await folderMap(path);
        if (request !== loadRequestRef.current) return null;
        if (!next.generatedAt || next.sizeKnown === false) {
          setScanning(true);
          setProgress({ phase: 'folder-map', requestID: progressRequestID, filesScanned: 0, path });
          next = await measureFolderMap(path, progressRequestID);
        }
      } else {
        next = await folderMap(path);
      }
      if (request !== loadRequestRef.current) return null;
      mapCacheRef.current.set(path, next);
      mapCacheRef.current.set(next.path, next);
      if (next.generatedAt) updateParentCache(next);
      acceptMap(next);
      setVirtualStack([]);
      setSelected(null);
      setHovered(null);
      return next;
    } catch (reason) {
      if (request === loadRequestRef.current) {
        setError(String(reason));
        if (remember && previousPath) setHistory(stack => stack.at(-1) === previousPath ? stack.slice(0, -1) : stack);
      }
      return null;
    } finally {
      if (request === loadRequestRef.current) {
        activeProgressRequestRef.current = '';
        setLoading(false);
        setScanning(false);
        setProgress(null);
      }
    }
  };

  const prefetchMap = path => {
    if (scanning) return;
    if (hasMeasuredFolderMap(mapCacheRef.current.get(path)) || prefetchingRef.current.has(path)) return;
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
    const cancel = onScanProgress(nextProgress => {
      if (nextProgress.phase === 'folder-map' && nextProgress.requestID === activeProgressRequestRef.current) {
        const count = Math.max(progressCountRef.current, Number(nextProgress.filesScanned || 0));
        progressCountRef.current = count;
        setProgress({ ...nextProgress, filesScanned: count });
      }
    });
    // Show the root's immediate entries first. On a cold cache, calculate its
    // top-level folder sizes progressively, then reuse that local snapshot.
    if (!initializedRef.current) {
      initializedRef.current = true;
      const initialize = async () => {
        try {
          const root = await getScanRoot();
          setScanRoot(root || '');
          if (!root) {
            setLoading(false);
            return;
          }
          storageInfo().then(setStorage).catch(() => {});
          const rootMap = await loadMap(root, false, true, false);
          if (rootMap && !rootMap.generatedAt) {
            await new Promise(resolve => requestAnimationFrame(resolve));
            await loadMap(rootMap.path, false, false, true);
          }
        } catch (reason) {
          setError(String(reason));
          setLoading(false);
        }
      };
      initialize();
    }
    return typeof cancel === 'function' ? cancel : undefined;
  }, []);

  const selectScanRoot = async () => {
    if (choosingRoot) return;
    setChoosingRoot(true);
    setError('');
    try {
      const root = await chooseScanRoot();
      if (!root || root === scanRoot) return;
      setScanRoot(root);
      setHistory([]);
      setVirtualStack([]);
      setSelected(null);
      setHovered(null);
      storageInfo().then(setStorage).catch(() => {});
      await loadMap(root, false, true, false);
    } catch (reason) {
      setError(String(reason));
    } finally {
      setChoosingRoot(false);
    }
  };

  const goBack = async () => {
    if (virtualStack.length) {
      loadRequestRef.current += 1;
      activeProgressRequestRef.current = '';
      setLoading(false);
      setScanning(false);
      setProgress(null);
      setVirtualStack(stack => stack.slice(0, -1));
      setSelected(null);
      setHovered(null);
      return;
    }
    const target = history.at(-1);
    if (target == null) return;
    setHistory(stack => stack.slice(0, -1));
    await loadMap(target, false, false, true);
  };

  const openCell = cell => {
    if (cell.virtual) {
      loadRequestRef.current += 1;
      activeProgressRequestRef.current = '';
      setLoading(false);
      setScanning(false);
      setProgress(null);
      setVirtualStack(stack => [...stack, { group: cell }]);
      setSelected(null);
      setHovered(null);
      return;
    }
    if (cell.directory) loadMap(cell.path, true, false, true);
  };

  const jumpTo = async path => {
    if (path === data?.path) return;
    const currentParts = scopedPathParts(data?.path, scanRoot).map(part => part.path);
    const index = currentParts.indexOf(path);
    if (index >= 0) {
      setHistory(currentParts.slice(0, index));
      setVirtualStack([]);
    }
    await loadMap(path, false, false, true);
  };

  const used = storage ? Math.max(0, Number(storage.total) - Number(storage.available)) : 0;
  const scanCount = Number(progress?.filesScanned || 0);
  const loadingText = scanning
    ? `폴더 크기 계산 중 · ${scanCount.toLocaleString()}개 파일 확인`
    : '폴더를 여는 중…';
  const viewData = virtualStack.length ? virtualStack[virtualStack.length - 1].group : data;
  const unmeasuredFolders = viewData?.children?.filter(child => child.directory && (
    child.sizeKnown === false || (child.sizeComplete === false && Number(child.bytes || 0) === 0)
  )).length || 0;
  const staleFolders = viewData?.children?.filter(child => child.directory && child.sizeStale).length || 0;
  const partialFolders = viewData?.children?.filter(child => child.directory && child.sizeKnown !== false && child.sizeComplete === false && !child.sizeStale && Number(child.bytes || 0) > 0).length || 0;
  const visibleItems = layoutEntries(viewData).length;
  const breadcrumbItems = data ? [
    ...scopedPathParts(data.path, scanRoot),
    ...virtualStack.map((entry, index) => ({ label: entry.group.name, virtualIndex: index, path: `virtual:${index}` })),
  ] : [];

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="brand">DiskAtlas</div>
        <nav className="breadcrumbs" aria-label="현재 경로">
          <button className="back" onClick={goBack} disabled={!history.length && !virtualStack.length} aria-label="뒤로">‹</button>
          {breadcrumbItems.map((part, index, all) => (
            <span key={part.path}>
              <button onClick={() => part.virtualIndex == null ? jumpTo(part.path) : setVirtualStack(stack => stack.slice(0, part.virtualIndex + 1))} disabled={index === all.length - 1}>{part.label}</button>
              {index > 0 && index < all.length - 1 && <i>/</i>}
            </span>
          ))}
        </nav>
        <div className="disk-summary">
          {storage && <span>{bytes(storage.available)} 여유 <small>{bytes(used)} 사용</small></span>}
          <button className="scope-button" onClick={selectScanRoot} disabled={choosingRoot || loading} title="분석할 폴더 선택">{choosingRoot ? '선택 중…' : '폴더 선택'}</button>
          <button className={`refresh-button${refreshing ? ' refreshing' : ''}`} onClick={reloadCurrentMap} disabled={loading || refreshing || !data || virtualStack.length > 0} aria-label="현재 폴더 새로고침" title="현재 폴더 새로고침">
            <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M16.4 8A6.7 6.7 0 0 0 4.6 5.5L3 7.1M3.2 3.7v3.6h3.6M3.6 12a6.7 6.7 0 0 0 11.8 2.5l1.6-1.6m-.2 3.4v-3.6h-3.6" /></svg>
          </button>
        </div>
      </header>

      <main className="workspace">
        <section className="map-header">
          <div>
            <h1>{viewData?.name || '디스크 지도'}</h1>
            <p>{viewData?.virtual
              ? `${bytes(viewData.bytes)} · ${Number(viewData.groupedCount || 0).toLocaleString()}개 항목`
              : viewData ? viewData.sizeComplete
              ? `${bytes(viewData.bytes)} · ${viewData.files.toLocaleString()}개 파일 · ${viewData.directories.toLocaleString()}개 폴더`
              : `${bytes(viewData.bytes)} 확인된 용량${staleFolders ? ` · ${staleFolders.toLocaleString()}개 저장된 측정값` : ''}${partialFolders ? ` · ${partialFolders.toLocaleString()}개 일부 확인` : ''}${unmeasuredFolders ? ` · ${unmeasuredFolders.toLocaleString()}개 폴더 용량 미확인` : ''}`
              : '용량을 면적으로, 최근 변경 시점을 색으로 표시합니다.'}</p>
          </div>
          <div className="legend" aria-label="색상 범례">
            <span className="age-now">7일</span><span className="age-month">30일</span><span className="age-half">6개월</span><span className="age-year">1년</span><span className="age-old">오래됨</span>
          </div>
        </section>

        <section className={`map-stage ${loading ? 'loading' : ''}`}>
          {viewData && <Treemap data={viewData} selected={selected} onSelect={setSelected} onHover={setHovered} onPrefetch={prefetchMap} onOpen={openCell} />}
          {hovered && <div className="map-hover-card" role="status"><strong>{hovered.name}</strong><span>{sizeLabel(hovered)} · {modifiedLabel(hovered.modifiedAt)}</span><small>{hovered.virtual ? `${hovered.groupedCount}개 합산 · 가상 폴더` : hovered.path}</small>{hovered.sizeStale && <em>저장된 측정값입니다. 상단에서 다시 계산할 수 있습니다.</em>}{hovered.directory && hovered.sizeComplete === false && !hovered.sizeStale && <em>일부 경로 또는 다른 볼륨을 제외한 최소 용량입니다</em>}{hovered.directory && <em>더블 클릭하여 열기</em>}</div>}
          {!data && !loading && <div className="first-run"><strong>분석할 위치를 선택하세요</strong><span>선택한 폴더와 하위 폴더의 용량 지도를 엽니다.</span><button onClick={selectScanRoot} disabled={choosingRoot}>{choosingRoot ? '선택 중…' : '폴더 선택'}</button></div>}
          {loading && <div className="map-loading" role="status" aria-live="polite"><span className="loading-spinner" /><span>{loadingText}</span>{scanning && progress?.path && <small>{progress.path}</small>}</div>}
        </section>

        <footer className="inspector">
          <div className="scan-status">
            {scanning ? <><span className="pulse" /> 폴더 크기 계산 중</> : viewData ? `${visibleItems.toLocaleString()}개 항목${staleFolders ? ` · ${staleFolders}개 저장된 측정값` : ''}${partialFolders ? ` · ${partialFolders}개 일부 확인` : ''}${unmeasuredFolders ? ` · ${unmeasuredFolders}개 폴더 용량 미확인` : ''}` : null}
          </div>
          {selected && <div className="selection"><strong>{selected.virtual ? selected.name : selected.path}</strong><span>{sizeLabel(selected)} · {modifiedLabel(selected.modifiedAt)}</span>{selected.directory && <button onClick={() => openCell(selected)}>열기</button>}{!selected.virtual && <button onClick={() => revealPath(selected.path)}>위치</button>}</div>}
        </footer>
        {error && <div className="error" role="alert">{error}</div>}
      </main>
    </div>
  );
}
