package main

import (
	"encoding/json"
	"time"
)

// MagnetLink 定义磁力链接结构
type MagnetLink struct {
	Infohash  string    `json:"infohash"`
	Magnet    string    `json:"magnet"`
	Discovered time.Time `json:"discovered"`
}

// 添加 MarshalJSON 方法自定义时间格式
func (m *MagnetLink) MarshalJSON() ([]byte, error) {
	type Alias MagnetLink
	return json.Marshal(&struct {
		Discovered string `json:"discovered"`
		*Alias
	}{
		Discovered: m.Discovered.Format(time.RFC3339),
		Alias:      (*Alias)(m),
	})
}