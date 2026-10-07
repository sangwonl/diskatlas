package core

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

type TrashMoveResult struct {
	EstimatedBytes     int64 `json:"estimatedBytes"`
	PendingSizeTracked bool  `json:"pendingSizeTracked"`
}

type trashReceipt struct {
	TrashedPath string `json:"trashedPath"`
	Volume      string `json:"volume"`
	Bytes       int64  `json:"bytes"`
}

type trashReceipts struct {
	Version int            `json:"version"`
	Items   []trashReceipt `json:"items"`
}

var trashReceiptsMu sync.Mutex

// MoveToTrash moves a selected item inside root to the operating system trash.
// It never falls back to permanent deletion.
func MoveToTrash(root, target string, estimatedBytes int64) (TrashMoveResult, error) {
	result := TrashMoveResult{}
	cleanRoot, err := analysisRoot(root)
	if err != nil {
		return result, err
	}
	cleanTarget, err := filepath.Abs(target)
	if err != nil {
		return result, err
	}
	cleanTarget = filepath.Clean(cleanTarget)
	if cleanTarget == cleanRoot || !isWithin(cleanRoot, cleanTarget) {
		return result, errors.New("분석 위치 자체 또는 범위 밖의 항목은 휴지통으로 보낼 수 없습니다")
	}
	info, err := os.Lstat(cleanTarget)
	if err != nil {
		return result, err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return result, errors.New("심볼릭 링크나 특수 파일은 휴지통으로 보낼 수 없습니다")
	}
	volume := trashVolumeID(cleanTarget)
	if volume == "" {
		return result, errors.New("항목이 있는 볼륨을 확인할 수 없습니다")
	}
	if estimatedBytes < 0 {
		estimatedBytes = 0
	}
	trashedPath, err := platformMoveToTrash(cleanTarget)
	if err != nil {
		return result, err
	}
	result.EstimatedBytes = estimatedBytes
	if trashedPath == "" {
		return result, nil
	}
	if err := addTrashReceipt(trashReceipt{TrashedPath: trashedPath, Volume: volume, Bytes: estimatedBytes}); err != nil {
		return result, nil
	}
	result.PendingSizeTracked = true
	return result, nil
}

func trashPendingBytes(path string) int64 {
	volume := trashVolumeID(path)
	if volume == "" {
		return 0
	}
	trashReceiptsMu.Lock()
	defer trashReceiptsMu.Unlock()
	receipts, err := readTrashReceipts()
	if err != nil {
		return 0
	}
	kept := make([]trashReceipt, 0, len(receipts.Items))
	var total int64
	changed := false
	for _, receipt := range receipts.Items {
		if _, err := os.Lstat(receipt.TrashedPath); errors.Is(err, fs.ErrNotExist) {
			changed = true
			continue
		}
		kept = append(kept, receipt)
		if receipt.Volume == volume && receipt.Bytes > 0 {
			total += receipt.Bytes
		}
	}
	if changed {
		receipts.Items = kept
		_ = writeTrashReceipts(receipts)
	}
	return total
}

func addTrashReceipt(receipt trashReceipt) error {
	trashReceiptsMu.Lock()
	defer trashReceiptsMu.Unlock()
	receipts, err := readTrashReceipts()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if receipts.Version == 0 {
		receipts = trashReceipts{Version: 1, Items: []trashReceipt{}}
	}
	receipts.Items = append(receipts.Items, receipt)
	return writeTrashReceipts(receipts)
}

func readTrashReceipts() (trashReceipts, error) {
	state, err := stateDirectory()
	if err != nil {
		return trashReceipts{}, err
	}
	data, err := os.ReadFile(filepath.Join(state, "trash-receipts.json"))
	if err != nil {
		return trashReceipts{}, err
	}
	var receipts trashReceipts
	if err := json.Unmarshal(data, &receipts); err != nil {
		return trashReceipts{}, err
	}
	return receipts, nil
}

func writeTrashReceipts(receipts trashReceipts) error {
	state, err := stateDirectory()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(receipts, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(state, "trash-receipts-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return replaceFile(temporary, filepath.Join(state, "trash-receipts.json"))
}
