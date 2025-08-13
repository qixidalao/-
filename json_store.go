package main

import (
	"encoding/json"
	"log"
	"os"
	"path"
	"sync"
)


// TorrentStore 定义磁力链存储接口
type TorrentStore interface {
	SaveMagnet(magnet *MagnetLink) error
	Exists(infohash string) bool
}

type jsonStore struct {
	dir    string
	file   *os.File
	mu     sync.Mutex
	hashes map[string]struct{}
	magnets []*MagnetLink // 新增：保存所有磁力链
}

func NewJsonStore(dir string) TorrentStore {
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("❌ 创建存储目录失败: %v", err)
		return nil
	}
	
	filename := path.Join(dir, "magnets.json")
	
	// 初始化哈希集合
	hashes := make(map[string]struct{})
	magnets := make([]*MagnetLink, 0)
	
	// 尝试加载现有磁力链
	if file, err := os.Open(filename); err == nil {
		defer file.Close()
		dec := json.NewDecoder(file)
		if err := dec.Decode(&magnets); err == nil {
			for _, m := range magnets {
				hashes[m.Infohash] = struct{}{}
			}
		}
	}
	
	log.Printf("📂 存储文件: %s, 已加载 %d 条磁力链", filename, len(hashes))
	
	// 打开文件用于写入（覆盖模式）
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Printf("❌ 打开存储文件失败: %v", err)
		return nil
	}
	
	// 立即写入已有数据
	if len(magnets) > 0 {
		enc := json.NewEncoder(file)
		enc.SetIndent("", "  ")
		if err := enc.Encode(magnets); err != nil {
			log.Printf("❌ 初始化存储文件失败: %v", err)
		}
	}
	
	return &jsonStore{
		dir:    dir,
		file:   file,
		hashes: hashes,
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
	
	// 清空文件并重新写入所有数据
	if _, err := j.file.Seek(0, 0); err != nil {
		return err
	}
	if err := j.file.Truncate(0); err != nil {
		return err
	}
	
	enc := json.NewEncoder(j.file)
	enc.SetIndent("", "  ") // 设置缩进，使输出更易读
	
	if err := enc.Encode(j.magnets); err != nil {
		return err
	}
	
	return j.file.Sync()
}