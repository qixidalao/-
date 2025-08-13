package main

import (
	"fmt"
)

// 构建磁力链接
func buildMagnetLink(infohash string) string {
	publicTrackers := []string{
		"udp://tracker.openbittorrent.com:80",
		"udp://tracker.publicbt.com:80",
		"udp://tracker.istole.it:6969",
		"udp://open.demonii.com:1337",
		"udp://tracker.coppersurfer.tk:6969",
	}

	magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s", infohash)
	
	// 添加 tracker 参数
	for _, tracker := range publicTrackers {
		magnet += "&tr=" + tracker
	}

	return magnet
}