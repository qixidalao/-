package main

import (
	"encoding/json"
	"os"
	"path"
	"sync"
)

type TorrentStore interface {
	SaveMagnet(magnet *MagnetLink) error
	Exists(infohash string) bool
	GetAllMagnets() []*MagnetLink      // 获取所有磁力链接的副本
	RewriteFile(newMagnets []*MagnetLink) error // 用新的磁力链接列表覆盖整个文件
}

type jsonStore struct {
	dir     string
	file    *os.File
	mu      sync.Mutex
	hashes  map[string]struct{}
	magnets []*MagnetLink
}

func NewJsonStore(dir string) TorrentStore {
	if err := os.MkdirAll(dir, 0755); err != nil {
		logWithColor(LogLevelError, "❌ 创建存储目录失败: %v", err)
		return nil
	}

	filename := path.Join(dir, "magnets.json")

	hashes := make(map[string]struct{})
	magnets := make([]*MagnetLink, 0)

	// 尝试读取现有文件内容到内存
	if file, err := os.Open(filename); err == nil {
		defer file.Close()
		dec := json.NewDecoder(file)
		// 忽略解码错误，因为文件可能是空的或损坏的
		if err := dec.Decode(&magnets); err == nil {
			for _, m := range magnets {
				hashes[m.Infohash] = struct{}{}
			}
		}
	}

	logWithColor(LogLevelInfo, "📂 存储文件: %s, 已加载 %d 条磁力链", filename, len(hashes))

	// 【关键修复】以读写和追加模式打开文件，不再清空 (O_TRUNC 已移除)
	// 使用 os.O_RDWR | os.O_CREATE 确保文件可读可写
	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		logWithColor(LogLevelError, "❌ 打开存储文件失败: %v", err)
		return nil
	}

	// 【关键修复】启动时不再重写文件，保留现有内容

	return &jsonStore{
		dir:     dir,
		file:    file,
		hashes:  hashes,
		magnets: magnets,
	}
}

func (j *jsonStore) Exists(infohash string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, exists := j.hashes[infohash]
	return exists
}

func (j *jsonStore) SaveMagnet(magnet *MagnetLink) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if _, exists := j.hashes[magnet.Infohash]; exists {
		return nil
	}

	j.hashes[magnet.Infohash] = struct{}{}
	j.magnets = append(j.magnets, magnet)

	// 调用通用的重写文件逻辑，避免代码重复
	return j.rewriteFileUnsafe()
}

// GetAllMagnets 安全地获取所有磁力链接的副本
func (j *jsonStore) GetAllMagnets() []*MagnetLink {
	j.mu.Lock()
	defer j.mu.Unlock()
	// 返回一个副本，防止外部修改影响内部状态
	magnetsCopy := make([]*MagnetLink, len(j.magnets))
	copy(magnetsCopy, j.magnets)
	return magnetsCopy
}

// RewriteFile 用新的磁力链接列表覆盖整个文件 (外部调用)
func (j *jsonStore) RewriteFile(newMagnets []*MagnetLink) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	// 更新内存中的状态
	newHashes := make(map[string]struct{})
	for _, m := range newMagnets {
		newHashes[m.Infohash] = struct{}{}
	}
	j.magnets = newMagnets
	j.hashes = newHashes

	logWithColor(LogLevelWarn, "💾 存储文件将更新，剩余 %d 条磁力链", len(j.magnets))
	return j.rewriteFileUnsafe()
}

// rewriteFileUnsafe 是一个内部方法，在已经持有锁的情况下执行文件写入
func (j *jsonStore) rewriteFileUnsafe() error {
	// 移动到文件开头
	if _, err := j.file.Seek(0, 0); err != nil {
		return err
	}
	// 清空当前文件内容
	if err := j.file.Truncate(0); err != nil {
		return err
	}

	// 如果列表为空，则直接返回，文件内容即为空
	if len(j.magnets) == 0 {
		return j.file.Sync()
	}
	
	enc := json.NewEncoder(j.file)
	enc.SetIndent("", "  ") // 使用两个空格进行缩进

	if err := enc.Encode(j.magnets); err != nil {
		return err
	}

	return j.file.Sync()
}


