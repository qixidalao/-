package main

import (
	"context"
	"os"
	"path"
	"time"

	"github.com/anacrolix/torrent"
)

// FetchAndSaveMetadata 尝试通过infohash获取元数据并保存为.torrent文件
// 如果成功，返回true；否则返回false
func FetchAndSaveMetadata(infohash, torrentsDir string) bool {
	cfg := torrent.NewDefaultClientConfig()
	cfg.NoUpload = true // 我们只下载元数据，不进行上传
	cfg.DisableTCP = false
	cfg.DisableUTP = true // 通常TCP更可靠
	cfg.ListenPort = 0    // 不需要监听
	cfg.DataDir = os.TempDir() // 临时数据目录，因为我们不下载文件内容

	client, err := torrent.NewClient(cfg)
	if err != nil {
		logWithColor(LogLevelError, "❌ 创建BT客户端失败: %v", err)
		return false
	}
	defer client.Close()

	// 解析磁力链接
	magnetURI := buildMagnetLink(infohash)
	t, err := client.AddMagnet(magnetURI)
	if err != nil {
		logWithColor(LogLevelWarn, "⚠️ 添加磁力链接失败 (%s): %v", infohash[:8], err)
		return false
	}

	// 设置一个超时上下文，防止永久等待
	_, cancel := context.WithTimeout(context.Background(), 90*time.Second) // 90秒超时
	defer cancel()

	// 等待获取到info
	<-t.GotInfo()

	// 检查是否真的获取到了
	info := t.Info()
	if info == nil {
		logWithColor(LogLevelDebug, "⌛️ 获取元数据超时或失败: %s", infohash[:8])
		return false
	}

	// 成功获取，创建 .torrent 文件
	torrentFilePath := path.Join(torrentsDir, infohash+".torrent")
	f, err := os.Create(torrentFilePath)
	if err != nil {
		logWithColor(LogLevelError, "❌ 创建.torrent文件失败: %v", err)
		return false
	}
	defer f.Close()

	// 将元数据写入文件
	err = info.Write(f)
	if err != nil {
		logWithColor(LogLevelError, "❌ 写入.torrent文件失败: %v", err)
		// 写入失败，删除可能已创建的空文件
		os.Remove(torrentFilePath)
		return false
	}

	logWithColor(LogLevelInfo, "✅ 成功获取并保存元数据: %s.torrent", infohash)
	return true
}