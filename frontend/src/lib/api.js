import { FolderMap, MeasureFolderMap, RefreshFolderMap, ReloadFolderMap, RevealPath, StorageInfo } from '../../wailsjs/go/main/App';
import { EventsOn } from '../../wailsjs/runtime/runtime';

export const storageInfo = () => StorageInfo();
export const revealPath = path => RevealPath(path);
export const folderMap = path => FolderMap(path || '');
export const reloadFolderMap = path => ReloadFolderMap(path || '');
export const measureFolderMap = (path, requestID) => MeasureFolderMap(path || '', requestID);
export const refreshFolderMap = (path, requestID) => RefreshFolderMap(path || '', requestID);
export const onScanProgress = callback => EventsOn('scan:progress', callback);
