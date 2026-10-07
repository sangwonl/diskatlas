import { CancelFolderMap, ChooseScanRoot, FolderMap, MeasureFolderMap, OpenTrash, RefreshFolderMap, ReloadFolderMap, RevealPath, ScanRoot, StorageInfo, TrashPath } from '../../wailsjs/go/main/App';
import { EventsOn } from '../../wailsjs/runtime/runtime';

export const storageInfo = () => StorageInfo();
export const getScanRoot = () => ScanRoot();
export const chooseScanRoot = () => ChooseScanRoot();
export const revealPath = path => RevealPath(path);
export const trashPath = (path, estimatedBytes) => TrashPath(path, estimatedBytes);
export const openTrash = () => OpenTrash();
export const folderMap = path => FolderMap(path || '');
export const reloadFolderMap = path => ReloadFolderMap(path || '');
export const measureFolderMap = (path, requestID) => MeasureFolderMap(path || '', requestID);
export const refreshFolderMap = (path, requestID) => RefreshFolderMap(path || '', requestID);
export const cancelFolderMap = requestID => CancelFolderMap(requestID);
export const onScanProgress = callback => EventsOn('scan:progress', callback);
