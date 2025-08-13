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
		log.Printf("=== torsniff 启动 ===")
		log.Printf("监听地址: %s:%d", addr, port)
		log.Printf("最大节点数: %d", friends)
		log.Printf("存储目录: %s", absDir)
		log.Printf("详细日志: %t", verbose)
		log.Println("====================")

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

type CrawlerStatus struct {
	StartTime   time.Time
	MagnetCount int
	LastLogTime time.Time
}

type torsniff struct {
	laddr      string
	maxFriends int
	dir        string
	verbose    bool
}

func (p *torsniff) run() error {
	log.Println("🌱 正在初始化爬虫...")
	
	// 初始化存储
	store := NewJsonStore(p.dir)
	if store == nil {
		log.Fatal("❌ 无法初始化磁力链存储")
	}
	log.Println("💾 磁力链存储初始化完成")

	// 初始化DHT网络
	dht, err := newDHT(p.laddr, p.maxFriends)
	if err != nil {
		log.Fatalf("❌ DHT初始化失败: %v", err)
	}
	defer dht.Close()
	log.Println("🌐 DHT网络初始化完成")

	// 启动DHT
	go dht.run()
	log.Println("🚀 DHT网络已启动")

	// 创建状态记录器
	status := &CrawlerStatus{
		StartTime:   time.Now(),
		MagnetCount: 0,
		LastLogTime: time.Now(),
	}

	log.Println("====================================")
	log.Println("🏁 磁力链爬虫已启动，开始爬取数据...")
	log.Println("====================================")

	// 主循环
	for {
		select {
		case <-dht.die:
			log.Printf("⚠️ DHT网络异常终止: %v", dht.errDie)
			return dht.errDie
			
		case announce := <-dht.chAnnouncement:
			infohash := announce.infohashHex
			
			// 跳过重复项
			if store.Exists(infohash) {
				if p.verbose {
					log.Printf("⏭️ 跳过重复磁力链: %s...", infohash[:8])
				}
				continue
			}
			
			// 创建磁力链
			magnet := &MagnetLink{
				Infohash:  infohash,
				Magnet:    buildMagnetLink(infohash),
				Discovered: time.Now(),
			}
			
			// 保存磁力链
			if err := store.SaveMagnet(magnet); err != nil {
				log.Printf("❌ 保存磁力链失败: %v", err)
			} else {
				status.MagnetCount++
				log.Printf("✅ 发现新磁力链 [%04d]: magnet:?xt=urn:btih:%s", 
					status.MagnetCount, infohash)
			}
			
			// 加入查询队列
			dht.enqueueQuery(infohash)
			
		case <-time.After(30 * time.Second):
			// 每分钟打印状态
			if time.Since(status.LastLogTime) > time.Minute {
				duration := time.Since(status.StartTime)
				log.Printf("📊 状态统计: 运行 %s | 发现 %d 个磁力链 | 节点数 %d | 队列 %d",
					formatDuration(duration), 
					status.MagnetCount,
					len(dht.knownNodes),
					dht.queryQueue.Len())
				status.LastLogTime = time.Now()
			}
		}
	}
}