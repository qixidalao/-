package main

import (
	"encoding/json"
	"time"
)

type MagnetLink struct {
	Infohash   string    `json:"infohash"`
	Magnet     string    `json:"magnet"`
	Discovered time.Time `json:"discovered"`
}

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