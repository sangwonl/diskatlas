import React from 'react';
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { cancelFolderMap, chooseScanRoot, folderMap, getScanRoot, measureFolderMap, onScanProgress, openTrash, refreshFolderMap, reloadFolderMap, revealPath, storageInfo, trashPath } from './lib/api';
import { BrowserOpenURL } from '../wailsjs/runtime/runtime';
import { layoutTreemap } from './lib/treemap';
import { appVersion, checkForUpdate, updatePreview } from './lib/updates';

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

function errorMessage(reason) {
  const message = reason instanceof Error ? reason.message : String(reason ?? '');
  return message.replace(/^Error:\s*/, '');
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
  return /^[A-Za-z]:/.test(normalized) || normalized.startsWith('//') ? normalized.toLowerCase() : normalized;
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

function directionalCell(cells, viewport, index, key) {
  const horizontal = key === 'ArrowLeft' || key === 'ArrowRight';
  const direction = key === 'ArrowRight' || key === 'ArrowDown' ? 1 : -1;
  const origin = cells[index];
  if (!origin) return null;
  const originRect = {
    left: origin.x * viewport.width / 100,
    right: (origin.x + origin.width) * viewport.width / 100,
    top: origin.y * viewport.height / 100,
    bottom: (origin.y + origin.height) * viewport.height / 100,
  };
  const axisStart = horizontal ? 'left' : 'top';
  const axisEnd = horizontal ? 'right' : 'bottom';
  const crossStart = horizontal ? 'top' : 'left';
  const crossEnd = horizontal ? 'bottom' : 'right';
  const originCenter = (originRect[axisStart] + originRect[axisEnd]) / 2;
  const originCrossCenter = (originRect[crossStart] + originRect[crossEnd]) / 2;
  let nearest = null;
  let nearestScore = Infinity;
  const opposite = [];
  cells.forEach((candidate, candidateIndex) => {
    if (candidateIndex === index) return;
    const rect = {
      left: candidate.x * viewport.width / 100,
      right: (candidate.x + candidate.width) * viewport.width / 100,
      top: candidate.y * viewport.height / 100,
      bottom: (candidate.y + candidate.height) * viewport.height / 100,
    };
    const center = (rect[axisStart] + rect[axisEnd]) / 2;
    const crossCenter = (rect[crossStart] + rect[crossEnd]) / 2;
    const primary = (center - originCenter) * direction;
    if (primary <= 0) {
      opposite.push({ cell: candidate, index: candidateIndex, center, crossCenter });
      return;
    }
    const primaryGap = direction > 0
      ? Math.max(0, rect[axisStart] - originRect[axisEnd])
      : Math.max(0, originRect[axisStart] - rect[axisEnd]);
    const crossGap = Math.max(0, rect[crossStart] - originRect[crossEnd], originRect[crossStart] - rect[crossEnd]);
    const score = primaryGap + crossGap * 3 + Math.abs(crossCenter - originCrossCenter) * 0.05;
    if (score < nearestScore) {
      nearest = { cell: candidate, index: candidateIndex };
      nearestScore = score;
    }
  });
  if (nearest) return nearest;

  // Wrap at the outer edge of the map so repeated arrows keep moving focus.
  if (!opposite.length) return null;
  const oppositeEdge = direction > 0
    ? Math.min(...opposite.map(candidate => candidate.center))
    : Math.max(...opposite.map(candidate => candidate.center));
  return opposite
    .filter(candidate => Math.abs(candidate.center - oppositeEdge) < 0.5)
    .sort((a, b) => Math.abs(a.crossCenter - originCrossCenter) - Math.abs(b.crossCenter - originCrossCenter))[0];
}

const MIN_TILE_AREA_PX = 4500;
const MIN_TILE_WIDTH_PX = 100;
const MIN_TILE_HEIGHT_PX = 56;
const VIRTUAL_LAYOUT_SCALE = 0.35;
const MAX_VIRTUAL_AREA_SHARE = 0.03;
const TRASH_HOLD_MS = 2000;

function HoldToTrashButton({ disabled, busy, onConfirm, hotkeyProgress = 0, shortcutLabel }) {
  const [progress, setProgress] = useState(0);
  const startedAtRef = useRef(0);
  const frameRef = useRef(0);
  const confirmRef = useRef(onConfirm);
  confirmRef.current = onConfirm;

  const cancel = () => {
    startedAtRef.current = 0;
    if (frameRef.current) cancelAnimationFrame(frameRef.current);
    frameRef.current = 0;
    setProgress(0);
  };
  const visibleProgress = Math.max(progress, hotkeyProgress);

  const tick = () => {
    if (!startedAtRef.current) return;
    const elapsed = performance.now() - startedAtRef.current;
    const next = Math.min(1, elapsed / TRASH_HOLD_MS);
    setProgress(next);
    if (next >= 1) {
      startedAtRef.current = 0;
      frameRef.current = 0;
      setProgress(0);
      confirmRef.current();
      return;
    }
    frameRef.current = requestAnimationFrame(tick);
  };

  const start = event => {
    event.preventDefault();
    if (event.pointerType === 'mouse' && event.button !== 0) return;
    if (disabled || busy || startedAtRef.current) return;
    startedAtRef.current = performance.now();
    frameRef.current = requestAnimationFrame(tick);
  };

  useEffect(() => () => {
    if (frameRef.current) cancelAnimationFrame(frameRef.current);
  }, []);

  return (
    <button
      type="button"
      className="hold-to-trash"
      style={{ '--hold-progress': `${visibleProgress * 100}%` }}
      disabled={disabled || busy}
      onPointerDown={start}
      onPointerUp={cancel}
      onPointerCancel={cancel}
      onPointerLeave={cancel}
      onContextMenu={event => event.preventDefault()}
      onKeyDown={event => { if (event.code === 'Space' && !event.repeat) start(event); }}
      onKeyUp={event => { if (event.code === 'Space') cancel(); }}
      onBlur={cancel}
      onClick={event => event.preventDefault()}
      title={`2초 유지: ${shortcutLabel} 또는 버튼을 길게 눌러 휴지통으로 이동`}
      aria-label={busy ? '휴지통으로 이동 중' : `2초 동안 버튼을 누르거나 ${shortcutLabel} 단축키를 유지해 휴지통으로 이동`}
    >
      <span>{busy ? '이동 중…' : visibleProgress > 0 ? '계속 누르면 휴지통으로 이동' : '2초 눌러 휴지통으로'}</span>
      <kbd>{shortcutLabel}</kbd>
    </button>
  );
}

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

function Treemap({ data, selected, onSelect, onOpen, onBack, onHover, onPrefetch }) {
  const mapRef = useRef(null);
  const cellRefs = useRef([]);
  const previousPathRef = useRef(data.path);
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
  const activeIndex = Math.max(0, cells.findIndex(cell => cell.path === selected?.path));
  useLayoutEffect(() => {
    if (previousPathRef.current === data.path) return;
    previousPathRef.current = data.path;
    cellRefs.current[0]?.focus();
  }, [data.path, cells]);
  const moveFocus = (index, cell) => {
    if (index < 0 || !cell) return;
    onSelect(cell);
    requestAnimationFrame(() => cellRefs.current[index]?.focus());
  };
  const handleCellKeyDown = (event, cell, index) => {
    if (['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) {
      event.preventDefault();
      const next = directionalCell(cells, viewport, index, event.key);
      if (next) moveFocus(next.index, next.cell);
      return;
    }
    if (event.key === 'Enter') {
      event.preventDefault();
      onSelect(cell);
      if (cell.directory) onOpen(cell);
      return;
    }
    if (event.key === 'Backspace' && (event.metaKey || event.ctrlKey)) return;
    if (event.key === 'Backspace' || event.key === 'Escape') {
      event.preventDefault();
      onBack();
    }
  };
  useEffect(() => {
    const isEditableTarget = target => Boolean(target?.closest?.('input, textarea, select, [contenteditable="true"], [role="textbox"]'));
    const onKeyDown = event => {
      if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return;
      if (event.target?.closest?.('.map-cell') || isEditableTarget(event.target) || !cells.length) return;
      event.preventDefault();
      const selectedIndex = cells.findIndex(cell => cell.path === selected?.path);
      const originIndex = selectedIndex >= 0 ? selectedIndex : 0;
      const next = directionalCell(cells, viewport, originIndex, event.key) || { cell: cells[originIndex], index: originIndex };
      onSelect(next.cell);
      requestAnimationFrame(() => cellRefs.current[next.index]?.focus());
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [cells, onSelect, selected?.path, viewport]);
  if (!cells.length) return null;
  return (
    <div ref={mapRef} className="treemap" role="tree" aria-label={`${data.path} 용량 지도. 방향키로 이동, Enter로 폴더 열기, Backspace 또는 Escape로 뒤로 가기`}>
      {cells.map((cell, index) => {
        const density = Math.max(cell.virtual ? 1 : 0, cellDensity(cell, viewport));
        return (
          <button
            type="button"
            role="treeitem"
            key={cell.path}
            ref={node => { cellRefs.current[index] = node; }}
            className={`map-cell density-${density} ${cell.sizeKnown === false ? 'size-unknown' : ageClass(cell.modifiedAt)} ${selected?.path === cell.path ? 'selected' : ''}`}
            tabIndex={index === activeIndex ? 0 : -1}
            style={{ left: `${cell.x}%`, top: `${cell.y}%`, width: `${cell.width}%`, height: `${cell.height}%` }}
            onClick={() => onSelect(cell)}
            onDoubleClick={() => cell.directory && onOpen(cell)}
            onKeyDown={event => handleCellKeyDown(event, cell, index)}
            onMouseEnter={() => { onHover(cell); if (cell.directory && !cell.virtual) onPrefetch(cell.path); }}
            onMouseLeave={() => onHover(null)}
            onFocus={() => { onHover(cell); onSelect(cell); }}
            onBlur={() => onHover(null)}
            title={`${cell.virtual ? cell.name : cell.path}\n${sizeLabel(cell)} · ${modifiedLabel(cell.modifiedAt)}`}
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
  const [notice, setNotice] = useState('');
  const [toastLeaving, setToastLeaving] = useState(false);
  const [choosingRoot, setChoosingRoot] = useState(false);
  const [availableUpdate, setAvailableUpdate] = useState(null);
  const [trashing, setTrashing] = useState(false);
  const [trashHotkeyProgress, setTrashHotkeyProgress] = useState(0);
  const trashHotkeyRef = useRef({ startedAt: 0, frame: 0, code: '', path: '', requiresModifier: false, progressBucket: 0 });
  const trashHotkeyActionRef = useRef(null);

  const cancelActiveFolderMeasurement = () => {
    const requestID = activeProgressRequestRef.current;
    if (!requestID) return;
    activeProgressRequestRef.current = '';
    cancelFolderMap(requestID).catch(() => {});
  };

  useEffect(() => {
    let active = true;
    let checking = false;
    let retryAttempt = 0;
    let retryTimer = 0;
    const retryDelays = [30 * 1000, 2 * 60 * 1000, 10 * 60 * 1000, 30 * 60 * 1000];
    const scheduleRetry = () => {
      if (!active || retryTimer) return;
      const delay = retryDelays[Math.min(retryAttempt, retryDelays.length - 1)];
      retryAttempt += 1;
      retryTimer = window.setTimeout(() => {
        retryTimer = 0;
        check();
      }, delay);
    };
    const check = async () => {
      if (!active || checking) return;
      if (retryTimer) {
        window.clearTimeout(retryTimer);
        retryTimer = 0;
      }
      checking = true;
      let failed = false;
      try {
        const update = updatePreview
          ? { version: '0.1.1', url: 'https://github.com/sangwonl/diskatlas/releases/latest' }
          : await checkForUpdate();
        if (!active || !update) return;
        const dismissedVersion = window.localStorage.getItem('diskatlas-dismissed-update');
        if (dismissedVersion !== update.version) setAvailableUpdate(update);
      } catch (reason) {
        failed = true;
        console.warn('DiskAtlas update check failed:', reason);
      } finally {
        checking = false;
        if (!active) return;
        if (failed) scheduleRetry();
        else retryAttempt = 0;
      }
    };
    check();
    window.addEventListener('focus', check);
    const interval = window.setInterval(check, 6 * 60 * 60 * 1000);
    return () => {
      active = false;
      window.removeEventListener('focus', check);
      window.clearInterval(interval);
      if (retryTimer) window.clearTimeout(retryTimer);
    };
  }, []);

  useEffect(() => {
    const refreshStorage = () => {
      storageInfo().then(setStorage).catch(() => {});
    };
    window.addEventListener('focus', refreshStorage);
    return () => window.removeEventListener('focus', refreshStorage);
  }, []);

  useEffect(() => {
    if (!error && !notice) {
      setToastLeaving(false);
      return undefined;
    }
    setToastLeaving(false);
    const fadeTimeout = window.setTimeout(() => setToastLeaving(true), 4700);
    const dismissTimeout = window.setTimeout(() => {
      setError('');
      setNotice('');
    }, 5000);
    return () => {
      window.clearTimeout(fadeTimeout);
      window.clearTimeout(dismissTimeout);
    };
  }, [error, notice]);

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
      if (request === loadRequestRef.current) setError(errorMessage(reason));
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
    cancelActiveFolderMeasurement();
    const key = path || data?.root || currentPathRef.current || scanRoot || '/';
    const cached = mapCacheRef.current.get(key);
    if (cached && !force && hasMeasuredFolderMap(cached) && (!measure || cached.sizeKnown !== false)) {
      loadRequestRef.current += 1;
      activeProgressRequestRef.current = '';
      if (remember && data) setHistory(stack => [...stack, data.path]);
      acceptMap(cached);
      setScanning(false);
      setProgress(null);
      setRefreshing(false);
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
        setError(errorMessage(reason));
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
          setError(errorMessage(reason));
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
      setError(errorMessage(reason));
    } finally {
      setChoosingRoot(false);
    }
  };

  const goBack = async () => {
    if (virtualStack.length) {
      cancelActiveFolderMeasurement();
      loadRequestRef.current += 1;
      activeProgressRequestRef.current = '';
      setLoading(false);
      setRefreshing(false);
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

  const openCell = async cell => {
    if (cell.virtual) {
      cancelActiveFolderMeasurement();
      loadRequestRef.current += 1;
      activeProgressRequestRef.current = '';
      setLoading(false);
      setRefreshing(false);
      setScanning(false);
      setProgress(null);
      setVirtualStack(stack => [...stack, { group: cell }]);
      setSelected(null);
      setHovered(null);
      return;
    }
    if (cell.directory) {
      await loadMap(cell.path, true, false, true);
    }
  };

  const jumpTo = async path => {
    if (pathKey(path) === pathKey(data?.path)) {
      // A virtual directory changes the displayed map without changing data.path.
      // Clicking that real-directory breadcrumb should leave the virtual stack.
      if (virtualStack.length) {
        setVirtualStack([]);
        setSelected(null);
        setHovered(null);
      }
      return;
    }
    // FolderMap.Root is the canonical root used by the backend. The selected
    // scan path can be a symlink (for example /Users -> /System/Volumes/Data/Users),
    // so use the canonical root when deciding which breadcrumb ancestors are in scope.
    const currentParts = scopedPathParts(data?.path, data?.root || scanRoot).map(part => part.path);
    const index = currentParts.findIndex(part => pathKey(part) === pathKey(path));
    if (index >= 0) {
      setHistory(currentParts.slice(0, index));
      setVirtualStack([]);
    }
    await loadMap(path, false, false, true);
  };

  const dismissAvailableUpdate = () => {
    if (availableUpdate) window.localStorage.setItem('diskatlas-dismissed-update', availableUpdate.version);
    setAvailableUpdate(null);
  };

  const trashSelected = async () => {
    if (!selected || selected.virtual || !data || trashing) return;
    const target = selected;
    const currentDirectory = data.path;
    const invalidateAffectedMaps = () => {
      let path = currentDirectory;
      while (path) {
        mapCacheRef.current.delete(path);
        if (pathKey(path) === pathKey(scanRoot)) break;
        path = parentPath(path);
      }
    };
    setTrashing(true);
    setError('');
    setNotice('');
    try {
      const result = await trashPath(target.path, Number(target.bytes || 0));
      let refreshFailed = false;
      if (currentPathRef.current === currentDirectory) {
        setSelected(null);
        try {
          const next = await reloadFolderMap(currentDirectory);
          if (next && currentPathRef.current === currentDirectory) {
            mapCacheRef.current.set(next.path, next);
            updateParentCache(next);
            acceptMap(next);
          } else {
            invalidateAffectedMaps();
          }
        } catch {
          refreshFailed = true;
          invalidateAffectedMaps();
        }
      } else {
        invalidateAffectedMaps();
      }
      const nextStorage = await storageInfo().catch(() => null);
      if (nextStorage) setStorage(nextStorage);
      if (result && !result.pendingSizeTracked) {
        setNotice('휴지통으로 이동했어요. 이 항목의 대기 용량은 현재 확인할 수 없습니다.');
      } else if (refreshFailed) {
        setNotice('휴지통으로 이동했지만 현재 폴더 목록을 갱신하지 못했어요. 새로고침해 주세요.');
      } else {
        setNotice('휴지통으로 이동했어요. 공간은 휴지통을 비운 뒤 확보됩니다.');
      }
    } catch (reason) {
      setError(errorMessage(reason));
      setNotice('');
    } finally {
      setTrashing(false);
    }
  };

  const showTrash = async () => {
    storageInfo().then(setStorage).catch(() => {});
    try {
      await openTrash();
    } catch (reason) {
      setError(errorMessage(reason));
    }
  };

  const canTrashSelected = Boolean(
    selected && !selected.virtual && !selected.symlink && data &&
    selected.path !== data.root && !loading && !refreshing && !trashing
  );
  trashHotkeyActionRef.current = {
    canTrash: canTrashSelected,
    path: selected?.path || '',
    run: trashSelected,
  };

  useEffect(() => {
    const active = trashHotkeyRef.current;
    const cancelHold = () => {
      if (active.frame) cancelAnimationFrame(active.frame);
      active.startedAt = 0;
      active.frame = 0;
      active.code = '';
      active.path = '';
      active.requiresModifier = false;
      active.progressBucket = 0;
      setTrashHotkeyProgress(0);
    };
    const tick = () => {
      if (!active.startedAt) return;
      const action = trashHotkeyActionRef.current;
      if (!action?.canTrash || action.path !== active.path) {
        cancelHold();
        return;
      }
      const elapsed = performance.now() - active.startedAt;
      const progress = Math.min(1, elapsed / TRASH_HOLD_MS);
      const bucket = Math.floor(progress * 30);
      if (bucket !== active.progressBucket) {
        active.progressBucket = bucket;
        setTrashHotkeyProgress(progress);
      }
      if (progress >= 1) {
        active.startedAt = 0;
        active.frame = 0;
        active.code = '';
        active.path = '';
        active.requiresModifier = false;
        active.progressBucket = 0;
        setTrashHotkeyProgress(0);
        action.run();
        return;
      }
      active.frame = requestAnimationFrame(tick);
    };
    const isEditableTarget = target => Boolean(target?.closest?.('input, textarea, select, [contenteditable="true"], [role="textbox"]'));
    const onKeyDown = event => {
      const plainDelete = event.key === 'Delete' && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey;
      const modifiedBackspace = event.key === 'Backspace' && (event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey;
      if (!plainDelete && !modifiedBackspace) return;
      if (isEditableTarget(event.target)) return;
      const action = trashHotkeyActionRef.current;
      if (!action?.canTrash) return;
      if (event.repeat) {
        if (active.startedAt && active.code === event.code) event.preventDefault();
        return;
      }
      if (active.startedAt) return;
      event.preventDefault();
      active.startedAt = performance.now();
      active.code = event.code;
      active.path = action.path;
      active.requiresModifier = modifiedBackspace;
      active.progressBucket = 0;
      setTrashHotkeyProgress(0);
      active.frame = requestAnimationFrame(tick);
    };
    const onKeyUp = event => {
      if (!active.startedAt) return;
      const releasedShortcutKey = event.code === active.code;
      const releasedModifier = active.requiresModifier && /^(Meta|Control)/.test(event.code);
      if (releasedShortcutKey || releasedModifier) cancelHold();
    };
    const onWindowBlur = () => cancelHold();

    window.addEventListener('keydown', onKeyDown);
    window.addEventListener('keyup', onKeyUp);
    window.addEventListener('blur', onWindowBlur);
    return () => {
      window.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('keyup', onKeyUp);
      window.removeEventListener('blur', onWindowBlur);
      if (active.frame) cancelAnimationFrame(active.frame);
      active.startedAt = 0;
      active.frame = 0;
    };
  }, []);

  const used = storage ? Math.max(0, Number(storage.total) - Number(storage.available)) : 0;
  const scanCount = Number(progress?.filesScanned || 0);
  const loadingText = scanning
    ? `폴더 크기 계산 중 · ${scanCount.toLocaleString()}개 파일 확인`
    : '폴더를 여는 중…';
  const viewData = virtualStack.length ? virtualStack[virtualStack.length - 1].group : data;
  const visibleItems = layoutEntries(viewData).length;
  const isMacOS = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || '');
  const revealButtonLabel = isMacOS ? 'Finder에서 보기' : '탐색기에서 보기';
  const trashShortcutLabel = isMacOS ? '⌘+Backspace / Delete' : 'Ctrl+Backspace / Delete';
  const breadcrumbItems = data ? [
    ...scopedPathParts(data.path, data.root || scanRoot),
    ...virtualStack.map((entry, index) => ({ label: entry.group.name, virtualIndex: index, path: `virtual:${index}` })),
  ] : [];

  return (
    <div className={`app-shell${availableUpdate ? ' has-update' : ''}`}>
      {availableUpdate && <aside className="update-notice" role="status" aria-live="polite">
        <span className="update-notice-icon" aria-hidden="true">
          <svg viewBox="0 0 24 24"><path d="M12 3v12m0 0 4-4m-4 4-4-4M5 17v3h14v-3" /></svg>
        </span>
        <div className="update-notice-copy">
          <strong>DiskAtlas 업데이트가 있어요</strong>
          <span>현재 {appVersion} · 새 버전 {availableUpdate.version}. 릴리스 내용을 확인해보세요.</span>
        </div>
        <button className="update-notice-open" onClick={() => BrowserOpenURL(availableUpdate.url)}>릴리스 보기</button>
        <button className="update-notice-later" onClick={dismissAvailableUpdate}>나중에</button>
      </aside>}

      <header className="topbar">
        <div className="brand">DiskAtlas</div>
        <nav className="breadcrumbs" aria-label="현재 경로">
          <button type="button" className="back" onClick={goBack} disabled={!history.length && !virtualStack.length} aria-label="뒤로">‹</button>
          {breadcrumbItems.map((part, index, all) => (
            <span key={part.path}>
              <button
                type="button"
                title={part.virtualIndex == null ? part.path : part.label}
                aria-current={index === all.length - 1 ? 'location' : undefined}
                onClick={() => {
                  if (part.virtualIndex == null) jumpTo(part.path);
                  else {
                    setVirtualStack(stack => stack.slice(0, part.virtualIndex + 1));
                  }
                }}
                disabled={index === all.length - 1}
              >{part.label}</button>
              {index > 0 && index < all.length - 1 && <i>/</i>}
            </span>
          ))}
        </nav>
        <div className="disk-summary">
          {storage && <span className="storage-stat">{bytes(storage.available)} 여유 <small>{bytes(used)} 사용</small></span>}
          <button className="trash-summary" onClick={showTrash} title="DiskAtlas가 휴지통으로 보낸 항목 보기">
            <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M4 6h12m-10 0 .7 10h6.6L14 6M8 6V4h4v2m-3 3v4m2-4v4" /></svg>
            <span>{storage?.trashPending ? `≈ ${bytes(storage.trashPending)} 대기` : '휴지통'}</span>
          </button>
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
              : `${bytes(viewData.bytes)} 확인된 용량`
              : '용량을 면적으로, 최근 변경 시점을 색으로 표시합니다.'}</p>
          </div>
          <div className="legend" aria-label="색상 범례">
            <span className="age-now">7일</span><span className="age-month">30일</span><span className="age-half">6개월</span><span className="age-year">1년</span><span className="age-old">오래됨</span>
          </div>
        </section>

        <section className={`map-stage ${loading ? 'loading' : ''}`}>
          {viewData && <Treemap data={viewData} selected={selected} onSelect={setSelected} onBack={goBack} onHover={setHovered} onPrefetch={prefetchMap} onOpen={openCell} />}
          {hovered && <div className="map-hover-card" role="status"><strong>{hovered.name}</strong><span>{sizeLabel(hovered)} · {modifiedLabel(hovered.modifiedAt)}</span><small>{hovered.virtual ? `${hovered.groupedCount}개 합산 · 가상 폴더` : hovered.path}</small>{hovered.directory && <em>더블 클릭 또는 Enter로 열기</em>}</div>}
          {!data && !loading && <div className="first-run"><strong>분석할 위치를 선택하세요</strong><span>선택한 폴더와 하위 폴더의 용량 지도를 엽니다.</span><button onClick={selectScanRoot} disabled={choosingRoot}>{choosingRoot ? '선택 중…' : '폴더 선택'}</button></div>}
          {loading && <div className="map-loading" role="status" aria-live="polite"><span className="loading-spinner" /><span>{loadingText}</span>{scanning && progress?.path && <small>{progress.path}</small>}</div>}
        </section>

        <footer className={`inspector${selected ? ' has-selection' : ''}`}>
          <div className="scan-status">
            {scanning ? <><span className="pulse" /> 폴더 크기 계산 중</> : viewData ? `${visibleItems.toLocaleString()}개 항목` : null}
          </div>
          {selected && <div className="selection-panel">
            <div className="selection-line">
              <strong title={selected.path}>{selected.virtual ? selected.name : selected.path}</strong>
              <span>{sizeLabel(selected)} · {modifiedLabel(selected.modifiedAt)}</span>
              {!selected.virtual && <button className="reveal-path" onClick={() => revealPath(selected.path)}>{revealButtonLabel}</button>}
              {selected.symlink
                ? <span className="selection-blocked">심볼릭 링크는 이동할 수 없음</span>
                : !selected.virtual && <HoldToTrashButton disabled={selected.path === data?.root || loading || refreshing} busy={trashing} onConfirm={trashSelected} hotkeyProgress={trashHotkeyProgress} shortcutLabel={trashShortcutLabel} />}
            </div>
          </div>}
        </footer>
        {(error || notice) && <div className={`${error ? 'error' : 'notice'}${toastLeaving ? ' toast-leaving' : ''}`} role={error ? 'alert' : 'status'}>{error || notice}</div>}
      </main>
    </div>
  );
}
