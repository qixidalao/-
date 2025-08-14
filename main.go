package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
)

type CrawlerStatus struct {
	StartTime   time.Time
	MagnetCount int
	LastLogTime time.Time
	VideoCount  int
	GameCount   int
	ImageCount  int
	OtherCount  int
}

type torsniff struct {
	laddr      string
	maxFriends int
	dir        string
	verbose    bool
}

func main() {
	log.SetFlags(0)

	var addr string
	var port uint16
	var friends int
	var dir string
	var verbose bool

	home, err := homedir.Dir()
	if err != nil {
		log.Fatalf("❌ 获取用户目录失败: %v", err)
	}
	userHome := path.Join(home, "torsniff")

	root := &cobra.Command{
		Use:          "torsniff",
		Short:        "torsniff - DHT网络磁力链爬虫",
		SilenceUsage: true,
	}
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if dir == userHome && err != nil {
			return err
		}

		absDir, err := filepath.Abs(dir)
		if err != nil {
			return err
		}

		log.SetOutput(os.Stdout)
		logWithColor(LogLevelInfo, "=== torsniff 启动 ===")
		logWithColor(LogLevelInfo, "监听地址: %s:%d", addr, port)
		logWithColor(LogLevelInfo, "最大节点数: %d", friends)
		logWithColor(LogLevelInfo, "存储目录: %s", absDir)
		logWithColor(LogLevelInfo, "详细日志: %t", verbose)
		logWithColor(LogLevelInfo, "====================")

		p := &torsniff{
			laddr:      net.JoinHostPort(addr, strconv.Itoa(int(port))),
			maxFriends: friends,
			dir:        absDir,
			verbose:    verbose,
		}

		return p.run()
	}

	root.Flags().StringVarP(&addr, "addr", "a", "0.0.0.0", "监听地址")
	root.Flags().Uint16VarP(&port, "port", "p", 9001, "监听端口")
	root.Flags().IntVarP(&friends, "friends", "f", 500, "最大DHT节点连接数")
	root.Flags().StringVarP(&dir, "dir", "d", "./magnets", "磁力链存储目录")
	root.Flags().BoolVarP(&verbose, "verbose", "v", true, "显示详细日志")

	if err := root.Execute(); err != nil {
		fmt.Println(fmt.Errorf("启动失败: %s", err))
	}
}

func (p *torsniff) run() error {
	logWithColor(LogLevelInfo, "🌱 正在初始化爬虫...")

	store := NewJsonStore(p.dir)
	if store == nil {
		log.Fatal("❌ 无法初始化磁力链存储")
	}
	logWithColor(LogLevelInfo, "💾 磁力链存储初始化完成")

	dht, err := newDHT(p.laddr, p.maxFriends)
	if err != nil {
		log.Fatalf("❌ DHT初始化失败: %v", err)
	}
	defer dht.Close()
	logWithColor(LogLevelInfo, "🌐 DHT网络初始化完成")

	go dht.run()
	logWithColor(LogLevelInfo, "🚀 DHT网络已启动")

	status := &CrawlerStatus{
		StartTime:   time.Now(),
		LastLogTime: time.Now(),
	}

	logWithColor(LogLevelInfo, "====================================")
	logWithColor(LogLevelInfo, "🏁 磁力链爬虫已启动，开始爬取数据...")
	logWithColor(LogLevelInfo, "====================================")

	for {
		select {
		case <-dht.die:
			logWithColor(LogLevelError, "⚠️ DHT网络异常终止: %v", dht.errDie)
			return dht.errDie

		case announce := <-dht.chAnnouncement:
			infohash := announce.infohashHex

			if store.Exists(infohash) {
				if p.verbose {
					log.Printf("⏭️ 跳过重复磁力链: %s...", infohash[:8])
				}
				continue
			}

			magnet := &MagnetLink{
				Infohash:   infohash,
				Magnet:     buildMagnetLink(infohash),
				Discovered: time.Now(),
			}

			if err := store.SaveMagnet(magnet); err != nil {
				log.Printf("❌ 保存磁力链失败: %v", err)
			} else {
				status.MagnetCount++
				logWithColor(LogLevelWarn, "✅ 发现新磁力链 [%04d]: magnet:?xt=urn:btih:%s",
					status.MagnetCount, infohash)
			}

			dht.enqueueQuery(infohash)

			category := classifyInfohash(infohash)
			status.addMagnet(category)

			logWithColor(LogLevelInfo, "发现%s磁力链 [%04d]: %s",
				getCategoryName(category), status.MagnetCount, infohash)

		case <-time.After(30 * time.Second):
			if time.Since(status.LastLogTime) > time.Minute {
				duration := time.Since(status.StartTime)
				dht.nodesMutex.Lock() // 在访问 dht.knownNodes 前加锁
				nodesCount := len(dht.knownNodes)
				dht.nodesMutex.Unlock() // 访问结束后解锁

				logWithColor(LogLevelInfo, "📊 状态统计: 运行 %s | 发现 %d 个磁力链 | 节点数 %d | 队列 %d",
					formatDuration(duration),
					status.MagnetCount,
					nodesCount, // 使用本地变量
					dht.queryQueue.Len())
				logWithColor(LogLevelInfo, "分类统计: 视频=%d, 游戏=%d, 图片/软件=%d, 其他=%d",
					status.VideoCount, status.GameCount, status.ImageCount, status.OtherCount)
				status.LastLogTime = time.Now()
			}

		}
	}
}

func classifyInfohash(infohash string) string {
	switch {
	case isVideoContent(infohash):
		return "video"
	case isGameContent(infohash):
		return "game"
	case isImageContent(infohash):
		return "image"
	default:
		return "other"
	}
}

func getCategoryName(category string) string {
	switch category {
	case "video":
		return "视频"
	case "game":
		return "游戏"
	case "image":
		return "图片/软件"
	default:
		return ""
	}
}

func (s *CrawlerStatus) addMagnet(category string) {
	switch category {
	case "video":
		s.VideoCount++
	case "game":
		s.GameCount++
	case "image":
		s.ImageCount++
	default:
		s.OtherCount++
	}
}
