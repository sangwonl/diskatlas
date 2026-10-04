package core

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FolderMap is one level of the cached filesystem tree. Children are sized
// from allocated bytes, so the frontend can render them as a treemap without
// walking the disk again.
type FolderMap struct {
	Root        string           `json:"root"`
	Path        string           `json:"path"`
	Name        string           `json:"name"`
	Bytes       int64            `json:"bytes"`
	Files       int              `json:"files"`
	ModifiedAt  string           `json:"modifiedAt,omitempty"`
	GeneratedAt string           `json:"generatedAt,omitempty"`
	Children    []FolderMapEntry `json:"children"`
}

type FolderMapEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Bytes      int64  `json:"bytes"`
	Files      int    `json:"files"`
	ModifiedAt string `json:"modifiedAt,omitempty"`
	Directory  bool   `json:"directory"`
}

type folderMapNode struct {
	entry      FolderMapEntry
	children   []*folderMapNode
	modifiedAt int64
}

type folderMapBuild struct {
	done        chan struct{}
	cancel      chan struct{}
	root        string
	cache       map[string]*folderMapNode
	generatedAt string
	err         error
}

var folderMapBuilds = struct {
	sync.Mutex
	active map[string]*folderMapBuild
}{active: map[string]*folderMapBuild{}}

// CachedFolderMap derives a single directory level from the persisted file
// manifest. Navigating the map therefore stays fast and never starts a scan.
func CachedFolderMap(root, target string) (*FolderMap, error) {
	normalizedRoot, err := analysisRoot(root)
	if err != nil {
		return nil, err
	}
	normalizedTarget := normalizedRoot
	if strings.TrimSpace(target) != "" {
		normalizedTarget, err = filepath.Abs(target)
		if err != nil {
			return nil, err
		}
		normalizedTarget = filepath.Clean(normalizedTarget)
	}
	if !isWithin(normalizedRoot, normalizedTarget) {
		return nil, errors.New("분석 범위 밖의 경로입니다")
	}

	build := startFolderMapBuild(normalizedRoot)
	<-build.done
	if build.err != nil {
		return nil, build.err
	}
	node := build.cache[normalizedTarget]
	if node == nil {
		return nil, errors.New("해당 경로의 분석 결과가 없습니다")
	}
	children := make([]FolderMapEntry, 0, len(node.children))
	for _, child := range node.children {
		children = append(children, child.entry)
	}
	return &FolderMap{
		Root: normalizedRoot, Path: normalizedTarget, Name: node.entry.Name,
		Bytes: node.entry.Bytes, Files: node.entry.Files,
		ModifiedAt:  node.entry.ModifiedAt,
		GeneratedAt: build.generatedAt, Children: children,
	}, nil
}

// WarmFolderMap starts building a derived in-memory directory database. The
// work is chunked in the background and shared by all navigation requests.
// Calling it repeatedly is safe; an active build is reused.
func WarmFolderMap(root string) {
	if normalized, err := analysisRoot(root); err == nil {
		_ = startFolderMapBuild(normalized)
	}
}

// ResetFolderMap discards the derived tree after a new analysis is saved.
func ResetFolderMap(root string) {
	if normalized, err := analysisRoot(root); err == nil {
		folderMapBuilds.Lock()
		if current := folderMapBuilds.active[normalized]; current != nil {
			close(current.cancel)
		}
		delete(folderMapBuilds.active, normalized)
		folderMapBuilds.Unlock()
	}
}

func startFolderMapBuild(root string) *folderMapBuild {
	folderMapBuilds.Lock()
	if current := folderMapBuilds.active[root]; current != nil {
		folderMapBuilds.Unlock()
		return current
	}
	build := &folderMapBuild{done: make(chan struct{}), cancel: make(chan struct{}), root: root}
	folderMapBuilds.active[root] = build
	folderMapBuilds.Unlock()
	go func() {
		build.cache, build.generatedAt, build.err = buildFolderMapNodes(root, build.cancel)
		close(build.done)
	}()
	return build
}

func buildFolderMapNodes(root string, cancel <-chan struct{}) (map[string]*folderMapNode, string, error) {
	index := loadAnalysisIndex(root)
	if index == nil || len(index.Files) == 0 {
		return nil, "", errors.New("저장된 분석 결과가 없습니다. 먼저 분석을 시작하세요")
	}
	nodes := map[string]*folderMapNode{
		root: {entry: FolderMapEntry{Name: filepath.Base(root), Path: root, Directory: true}},
	}
	ensure := func(path string, directory bool) *folderMapNode {
		if node := nodes[path]; node != nil {
			node.entry.Directory = node.entry.Directory || directory
			return node
		}
		node := &folderMapNode{entry: FolderMapEntry{Name: filepath.Base(path), Path: path, Directory: directory}}
		nodes[path] = node
		return node
	}
	processed := 0
	for path, file := range index.Files {
		processed++
		if processed%2048 == 0 {
			select {
			case <-cancel:
				return nil, "", errors.New("폴더 맵 준비가 중단되었습니다")
			default:
			}
		}
		if !isWithin(root, path) || filepath.Clean(path) == root {
			continue
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		parts := strings.Split(relative, string(filepath.Separator))
		nodes[root].entry.Bytes += file.Bytes
		nodes[root].entry.Files++
		parentPath := root
		parent := nodes[root]
		for indexPart, part := range parts {
			childPath := filepath.Join(parentPath, part)
			child := ensure(childPath, indexPart < len(parts)-1)
			if indexPart == 0 || child.entry.Bytes == 0 {
				parent.children = append(parent.children, child)
			}
			child.entry.Bytes += file.Bytes
			child.entry.Files++
			if file.ModTime > child.modifiedAt {
				child.modifiedAt = file.ModTime
				child.entry.ModifiedAt = formatFolderMapTime(file.ModTime)
			}
			parentPath, parent = childPath, child
		}
	}
	for _, node := range nodes {
		node.children = uniqueFolderMapChildren(node.children)
		sort.Slice(node.children, func(i, j int) bool {
			if node.children[i].entry.Bytes == node.children[j].entry.Bytes {
				return strings.ToLower(node.children[i].entry.Name) < strings.ToLower(node.children[j].entry.Name)
			}
			return node.children[i].entry.Bytes > node.children[j].entry.Bytes
		})
	}
	return nodes, index.SavedAt.UTC().Format(time.RFC3339), nil
}

func uniqueFolderMapChildren(children []*folderMapNode) []*folderMapNode {
	seen := make(map[string]bool, len(children))
	result := children[:0]
	for _, child := range children {
		if seen[child.entry.Path] {
			continue
		}
		seen[child.entry.Path] = true
		result = append(result, child)
	}
	return result
}

func formatFolderMapTime(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.Unix(0, value).UTC().Format(time.RFC3339)
}
