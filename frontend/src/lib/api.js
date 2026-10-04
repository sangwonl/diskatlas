import { Analyze, FolderMap, RevealPath, StorageInfo } from '../../wailsjs/go/main/App';
import { EventsOn } from '../../wailsjs/runtime/runtime';

export const analyze = () => Analyze('');
export const storageInfo = () => StorageInfo();
export const revealPath = path => RevealPath(path);
export const folderMap = path => FolderMap(path || '');
export const onScanProgress = callback => EventsOn('scan:progress', callback);
