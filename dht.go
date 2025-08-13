package main

import (
	"bytes"
	"container/list"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"log"
	"net"
	"sync"
	"time"

	"github.com/marksamman/bencode"
)

type nodeID []byte

type announcements struct {
	mu    sync.Mutex
	ll    *list.List
	cache map[string]*list.Element // 用于快速查找
	limit int
	input chan struct{}
}

func (a *announcements) full() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ll.Len() >= a.limit
}

func (a *announcements) put(ac *announcement) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.ll.Len() >= a.limit {
		return
	}

	// 检查是否已存在
	if _, exists := a.cache[ac.infohashHex]; exists {
		return
	}

	e := a.ll.PushBack(ac)
	a.cache[ac.infohashHex] = e

	select {
	case a.input <- struct{}{}:
	default:
	}
}

type announcement struct {
	from        net.UDPAddr
	infohash    []byte
	infohashHex string
}

type dht struct {
	announcements  *announcements
	chAnnouncement chan *announcement
	die            chan struct{}
	errDie         error
	localID        nodeID
	conn           *net.UDPConn
	queryTypes     map[string]func(map[string]interface{}, net.UDPAddr)
	secret         []byte

	// 新增字段
	findNodeChan   chan string
	bootstrapNodes []*net.UDPAddr
	knownNodes     map[string]*net.UDPAddr
	queryQueue     *list.List
	queryMutex     sync.Mutex

	// 公告消费者控制
	announceTicker *time.Ticker
}

func (d *dht) Close() {
	d.conn.Close()
	if d.announceTicker != nil {
		d.announceTicker.Stop()
	}
}

func newDHT(laddr string, maxFriendsPerSec int) (*dht, error) {
	conn, err := net.ListenPacket("udp", laddr)
	if err != nil {
		return nil, err
	}
	// 在 newDHT 函数中添加
	// conn.SetDeadline(time.Now().Add(10 * time.Second))
	// _, err = conn.WriteToUDP([]byte("ping"), &net.UDPAddr{IP: net.ParseIP("8.8.8.8"), Port: 12345})
	d := &dht{
		announcements: &announcements{
			ll:    list.New(),
			cache: make(map[string]*list.Element),
			limit: maxFriendsPerSec * 10,
			input: make(chan struct{}, 1),
		},
		chAnnouncement: make(chan *announcement, 1000),
		localID:        randBytes(20),
		conn:           conn.(*net.UDPConn),
		die:            make(chan struct{}),
		secret:         randBytes(20),
		findNodeChan:   make(chan string, 100),
		bootstrapNodes: []*net.UDPAddr{
			// BitTorrent Mainline DHT nodes
			{IP: net.ParseIP("router.bittorrent.com"), Port: 6881},
			{IP: net.ParseIP("dht.transmissionbt.com"), Port: 6881},
			{IP: net.ParseIP("router.utorrent.com"), Port: 6881},
			{IP: net.ParseIP("dht.libtorrent.org"), Port: 25401},
			{IP: net.ParseIP("dht.aelitis.com"), Port: 6881},
			{IP: net.ParseIP("dht.vuze.com"), Port: 6881},

			// Other known public nodes
			{IP: net.ParseIP("router.bitcomet.com"), Port: 6881},
			{IP: net.ParseIP("bootstrap.jami.net"), Port: 4222},

			// IPV4
			{IP: net.ParseIP("82.221.103.244"), Port: 6881},
			{IP: net.ParseIP("87.121.121.2"), Port: 6881},
			{IP: net.ParseIP("87.248.163.48"), Port: 6881},
			{IP: net.ParseIP("131.202.240.222"), Port: 6881},
			{IP: net.ParseIP("200.223.19.6"), Port: 6881},
			{IP: net.ParseIP("200.223.19.7"), Port: 6881},
			{IP: net.ParseIP("212.129.33.250"), Port: 6881},

			// IPV6
			{IP: net.ParseIP("[2001:470:8c3a::346]"), Port: 6881},
			{IP: net.ParseIP("[2a02:2268:2001:1:1:1:1:1]"), Port: 6881},

			// 新增活跃节点
			{IP: net.ParseIP("67.215.246.10"), Port: 6881},   // router.bittorrent.com
			{IP: net.ParseIP("82.221.103.244"), Port: 6881},  // 可靠节点
			{IP: net.ParseIP("104.238.198.186"), Port: 6881}, // 新增节点
		},
		knownNodes:     make(map[string]*net.UDPAddr),
		queryQueue:     list.New(),
		announceTicker: time.NewTicker(100 * time.Millisecond),
	}

	d.queryTypes = map[string]func(map[string]interface{}, net.UDPAddr){
		"announce_peer": d.onAnnouncePeerQuery,
		"ping":          d.onPingQuery,
		"find_node":     d.onFindNodeQuery,
		"get_peers":     d.onGetPeersQuery,
	}

	return d, nil
}

func (d *dht) run() {
	log.Println("🚀 DHT网络启动中...")
	go d.listen()
	go d.discoverNodes()
	go d.processQueries()
	go d.consumeAnnouncements()

	// 添加状态日志
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	go func() {
		for {
			select {
			case <-d.die:
				return
			case <-ticker.C:
				log.Printf("📡 DHT状态: 节点数 %d, 队列 %d",
					len(d.knownNodes), d.queryQueue.Len())
			}
		}
	}()
}

// 消费公告，控制速率
func (d *dht) consumeAnnouncements() {
    log.Println("⏳ 启动公告消费者")
    for {
        select {
        case <-d.die:
            return
        case <-d.announceTicker.C:
            d.announcements.mu.Lock()
            if d.announcements.ll.Len() > 0 {
                elem := d.announcements.ll.Front()
                ac := elem.Value.(*announcement)
                d.announcements.ll.Remove(elem)
                delete(d.announcements.cache, ac.infohashHex)
                
                // 添加详细日志
                log.Printf("📨 发送公告: %s... (来源: %s)", 
                    ac.infohashHex[:8], ac.from.String())

                select {
                case d.chAnnouncement <- ac:
                    // 成功发送
                default:
                    log.Println("⚠️ 公告通道已满，丢弃公告")
                }
            }
            d.announcements.mu.Unlock()
        }
    }
}

func (d *dht) listen() {
    buf := make([]byte, 65535)
    for {
        n, addr, err := d.conn.ReadFromUDP(buf)
        if err != nil {
            log.Printf("UDP读取错误: %v", err)
            continue
        }
        
        // log.Printf("收到 %d 字节 UDP 数据包 (来源: %s)", n, addr.String())
        d.onMessage(buf[:n], *addr)
    }
}

func (d *dht) onMessage(data []byte, from net.UDPAddr) {
	dict, err := bencode.Decode(bytes.NewBuffer(data))
	if err != nil {
		return
	}

	y, ok := dict["y"].(string)
	if !ok {
		return
	}

	switch y {
	case "q":
		log.Println("收到查询请求", from, dict["q"])
		d.onQuery(dict, from)
	case "r": 
		// 添加响应处理
		// log.Println("收到响应", from, dict["t"])
		d.onResponse(dict, from)
	}
}

func (d *dht) onResponse(dict map[string]interface{}, from net.UDPAddr) {
    r, ok := dict["r"].(map[string]interface{})
    if !ok {
        return
    }

    // 处理所有可能的响应类型
    if _, ok := r["nodes"]; ok {
        // log.Println("处理 find_node 响应", from)
        d.onFindNodeResponse(dict, from)
    } else if _, ok := r["token"]; ok {
        log.Println("处理 get_peers 响应", from)
		d.onGetPeersResponse(dict, from)
        // 这里可以添加更多处理逻辑
    } else if _, ok := r["id"]; ok {
        log.Println("处理 ping 响应", from)
    }
}

// 新增函数：处理 get_peers 响应
func (d *dht) onGetPeersResponse(dict map[string]interface{}, from net.UDPAddr) {
    r := dict["r"].(map[string]interface{})
    t, _ := dict["t"].(string)
    log.Printf("收到 get_peers 响应: 事务ID=%s 来源=%s", t[:min(4, len(t))], from.String())
    
    // 1. 如果有 values 字段，表示包含 peer 列表
    if values, ok := r["values"].([]interface{}); ok {
        for _, v := range values {
            if peer, ok := v.(string); ok && len(peer) == 6 {
                ip := net.IP(peer[:4])
                port := binary.BigEndian.Uint16([]byte(peer[4:6]))
                log.Printf("发现 peer: %s:%d (来自 %s)", ip, port, from.String())
            }
        }
    }
    
    // 2. 如果有 nodes 字段，处理节点信息（与 find_node 响应相同）
    if nodes, ok := r["nodes"].(string); ok && len(nodes) > 0 {
        d.onFindNodeResponse(dict, from) // 复用现有函数
    }
    
    // 3. 记录 token（虽然爬虫不需要）
    if _, ok := r["token"].(string); ok {
		log.Println("收到 token，用于后续 announce_peer")
        // 保存 token 可用于后续 announce_peer（爬虫不需要）
    }
}

func (d *dht) onQuery(dict map[string]interface{}, from net.UDPAddr) {
	q, ok := dict["q"].(string)
	if !ok {
		return
	}

	if handle, ok := d.queryTypes[q]; ok {
		handle(dict, from)
	}
}

func (d *dht) onAnnouncePeerQuery(dict map[string]interface{}, from net.UDPAddr) {
	tid, ok := dict["t"].(string)
	if !ok {
		return
	}

	a, ok := dict["a"].(map[string]interface{})
	if !ok {
		return
	}

	token, ok := a["token"].(string)
	if !ok || !d.validateToken(token, from) {
		return
	}

	// 发送回复
	r := map[string]interface{}{
		"id": string(d.localID),
	}
	reply := map[string]interface{}{
		"t": tid,
		"y": "r",
		"r": r,
	}
	d.send(reply, from)

	if d.announcements.full() {
		return
	}

	// 处理公告
	if ac := d.summarize(dict, from); ac != nil {
		log.Printf("收到公告: 来自 %s, Infohash: %s", from.String(), ac.infohashHex)
		d.announcements.put(ac)
		select {
		case d.chAnnouncement <- ac:
		default:
		}
	}
}

func (d *dht) send(dict map[string]interface{}, to net.UDPAddr) error {
	_, err := d.conn.WriteToUDP(bencode.Encode(dict), &to)
	return err
}

func (d *dht) makeToken(from net.UDPAddr) string {
	s := sha1.New()
	s.Write([]byte(from.String()))
	s.Write(d.secret)
	return string(s.Sum(nil))
}

func (d *dht) validateToken(token string, from net.UDPAddr) bool {
	return token == d.makeToken(from)
}

func (d *dht) summarize(dict map[string]interface{}, from net.UDPAddr) *announcement {
    a, ok := dict["a"].(map[string]interface{})
    if !ok {
        log.Println("⚠️ 无效公告: 缺少 'a' 字段")
        return nil
    }

    infohash, ok := a["info_hash"].(string)
    if !ok {
        log.Println("⚠️ 无效公告: 缺少 'info_hash' 字段")
        return nil
    }
    
    if len(infohash) != 20 {
        log.Printf("⚠️ 无效公告: info_hash 长度错误 (%d != 20)", len(infohash))
        return nil
    }

    return &announcement{
        from:        from,
        infohash:    []byte(infohash),
        infohashHex: hex.EncodeToString([]byte(infohash)),
    }
}

// 发送 find_node 请求
func (d *dht) findNode(target string, to net.UDPAddr) {
	tid := randBytes(2)
	query := map[string]interface{}{
		"t": string(tid),
		"y": "q",
		"q": "find_node",
		"a": map[string]interface{}{
			"id":     string(d.localID),
			"target": target,
		},
	}
	d.send(query, to)
}

// 处理 find_node 响应
func (d *dht) onFindNodeResponse(dict map[string]interface{}, _ net.UDPAddr) {
    r, ok := dict["r"].(map[string]interface{})
    if !ok {
        log.Println("⚠️ 无效的find_node响应: 缺少 'r' 字段")
        return
    }

    nodes, ok := r["nodes"].(string)
    if !ok {
        log.Println("⚠️ 无效的find_node响应: 缺少 'nodes' 字段")
        return
    }

    // 详细日志输出
    // log.Printf("收到节点数据: 长度=%d, 内容=%x", len(nodes), nodes[:min(50, len(nodes))])
    
    if len(nodes)%26 != 0 {
        log.Printf("⚠️ 无效节点数据长度: %d (应为26的倍数)", len(nodes))
        return
    }

    for i := 0; i < len(nodes); i += 26 {
        if i+26 > len(nodes) {
            break
        }
        
        nodeData := nodes[i : i+26]
        id := nodeData[:20]
        ip := net.IP(nodeData[20:24])
        port := binary.BigEndian.Uint16([]byte(nodeData[24:26]))
        addr := &net.UDPAddr{IP: ip, Port: int(port)}
        
        d.knownNodes[string(id)] = addr
        // log.Printf("➕ 添加新节点: %s:%d (ID: %x)", ip, port, id)
    }
}

func min(a, b int) int {
    if a < b {
        return a
    }
    return b
}

func (d *dht) discoverNodes() {
	// 首先连接引导节点
	for _, addr := range d.bootstrapNodes {
		d.findNode(string(d.localID), *addr)
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.die:
			return
		case <-ticker.C:
			// 定期查找新节点
			log.Println("正在查找新节点...")
			if len(d.knownNodes) > 0 {
				// 随机选择一个已知节点
				var randomAddr *net.UDPAddr
				for _, addr := range d.knownNodes {
					randomAddr = addr
					break
				}

				// 查找随机目标
				target := randBytes(20)
				d.findNode(string(target), *randomAddr)

				// 查找热门磁力链 ? 添加这行调用
				d.getPeersForPopularTorrents(*randomAddr)
			}
		case target := <-d.findNodeChan:
			// 查找特定目标
			if len(d.knownNodes) > 0 {
				var randomAddr *net.UDPAddr
				for _, addr := range d.knownNodes {
					randomAddr = addr
					break
				}
				d.findNode(target, *randomAddr)
			}
		}
	}
}

// 添加这个方法：查询热门磁力链
func (d *dht) getPeersForPopularTorrents(to net.UDPAddr) {
	popularHashes := []string{
		"e2467cbf021192c241367b892230dc1e05c0580e", // Ubuntu
		"5a8062c076fa85e8056456929059040c2a1e4c5d", // Fedora
		"2081d049de3abf95b2338d4c2d0f6150e87e9d1e", // Debian
		"a88fda5954e89178c372716a6a78b8180ef4c1d3", // The Matrix
		"6a9759bffd5c0af65319979fb7832189f4f3c35d", // Inception
	}

	for _, hash := range popularHashes {
		d.getPeers(hash, to)
	}
}

// 发送 get_peers 请求 (用于查找磁力链)
func (d *dht) getPeers(infohash string, to net.UDPAddr) {
	tid := randBytes(2)
	query := map[string]interface{}{
		"t": string(tid),
		"y": "q",
		"q": "get_peers",
		"a": map[string]interface{}{
			"id":        string(d.localID),
			"info_hash": infohash,
		},
	}
	// log.Println(to.IP.String(), to.Port, "查询磁力链:", infohash)
	d.send(query, to)
}

// 查询处理器
func (d *dht) processQueries() {
	ticker := time.NewTicker(2 * time.Second) // 控制查询频率
	defer ticker.Stop()

	for {
		select {
		case <-d.die:
			return
		case <-ticker.C:
			if hash, ok := d.dequeueQuery(); ok {
				if len(d.knownNodes) > 0 {
					var randomAddr *net.UDPAddr
					for _, addr := range d.knownNodes {
						randomAddr = addr
						break
					}
					d.getPeers(hash, *randomAddr)
					log.Printf("查询磁力链: %s", hash)
				}
			}
		}
	}
}

func (d *dht) enqueueQuery(hash string) {
	d.queryMutex.Lock()
	defer d.queryMutex.Unlock()
	d.queryQueue.PushBack(hash)
}

func (d *dht) dequeueQuery() (string, bool) {
	d.queryMutex.Lock()
	defer d.queryMutex.Unlock()

	if d.queryQueue.Len() == 0 {
		return "", false
	}

	elem := d.queryQueue.Front()
	hash := elem.Value.(string)
	d.queryQueue.Remove(elem)
	return hash, true
}

// 处理ping查询
func (d *dht) onPingQuery(dict map[string]interface{}, from net.UDPAddr) {
	tid, ok := dict["t"].(string)
	if !ok {
		return
	}

	// 回复ping
	reply := map[string]interface{}{
		"t": tid,
		"y": "r",
		"r": map[string]interface{}{
			"id": string(d.localID),
		},
	}
	d.send(reply, from)
	log.Printf("🔄 响应ping请求: %s", from.String())
}

// 处理find_node查询
func (d *dht) onFindNodeQuery(dict map[string]interface{}, from net.UDPAddr) {
	tid, ok := dict["t"].(string)
	if !ok {
		return
	}

	a, ok := dict["a"].(map[string]interface{})
	if !ok {
		return
	}

	target, ok := a["target"].(string)
	if !ok || len(target) != 20 {
		return
	}

	// 回复find_node
	reply := map[string]interface{}{
		"t": tid,
		"y": "r",
		"r": map[string]interface{}{
			"id":    string(d.localID),
			"nodes": "",
		},
	}
	d.send(reply, from)
	// log.Printf("🔍 响应find_node请求: %s", from.String())
}

// 处理get_peers查询
func (d *dht) onGetPeersQuery(dict map[string]interface{}, from net.UDPAddr) {
	tid, ok := dict["t"].(string)
	if !ok {
		return
	}

	a, ok := dict["a"].(map[string]interface{})
	if !ok {
		return
	}

	infohash, ok := a["info_hash"].(string)
	if !ok || len(infohash) != 20 {
		return
	}

	// 生成token
	token := d.makeToken(from)
	reply := map[string]interface{}{
		"t": tid,
		"y": "r",
		"r": map[string]interface{}{
			"id":    string(d.localID),
			"token": token,
			"nodes": "",
		},
	}
	d.send(reply, from)
	log.Printf("📥 响应get_peers请求: %s (%s)", from.String(), infohash[:8])
}
