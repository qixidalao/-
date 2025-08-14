// dht.go
package main

import (
	"bytes"
	"container/list"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/marksamman/bencode"
)

type nodeID []byte

type announcements struct {
	mu    sync.Mutex
	ll    *list.List
	cache map[string]*list.Element
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

	findNodeChan   chan string
	bootstrapNodes []*net.UDPAddr

	announceTicker *time.Ticker

	knownNodes map[string]*nodeScore
	nodesMutex sync.Mutex // <-- 为 knownNodes 添加专用的互斥锁
	// 用于处理查询请求的队列
	queryQueue *list.List
	queryMutex sync.Mutex
	verbose    bool
}

type nodeScore struct {
	addr     *net.UDPAddr
	score    int
	lastSeen time.Time
}

const (
	LogLevelDebug = iota
	LogLevelInfo
	LogLevelWarn
	LogLevelError
)

func logWithColor(level int, format string, args ...interface{}) {
	var colorCode, prefix string

	switch level {
	case LogLevelDebug:
		colorCode = "\033[36m"
		prefix = "🐞 DEBUG"
	case LogLevelInfo:
		colorCode = "\033[32m"
		prefix = "ℹ️ INFO"
	case LogLevelWarn:
		colorCode = "\033[33m"
		prefix = "⚠️ WARN"
	case LogLevelError:
		colorCode = "\033[31m"
		prefix = "❌ ERROR"
	default:
		colorCode = "\033[0m"
		prefix = "💬 LOG"
	}

	msg := fmt.Sprintf(format, args...)
	fmt.Printf("%s[%s] %s\033[0m\n", colorCode, prefix, msg)
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
			{IP: net.ParseIP("router.bittorrent.com"), Port: 6881},
			{IP: net.ParseIP("dht.transmissionbt.com"), Port: 6881},
			{IP: net.ParseIP("router.utorrent.com"), Port: 6881},
			{IP: net.ParseIP("dht.libtorrent.org"), Port: 25401},
			{IP: net.ParseIP("dht.aelitis.com"), Port: 6881},
			{IP: net.ParseIP("dht.vuze.com"), Port: 6881},
			{IP: net.ParseIP("router.bitcomet.com"), Port: 6881},
			{IP: net.ParseIP("82.221.103.244"), Port: 6881},
			{IP: net.ParseIP("87.121.121.2"), Port: 6881},
			{IP: net.ParseIP("87.248.163.48"), Port: 6881},
			{IP: net.ParseIP("131.202.240.222"), Port: 6881},
			{IP: net.ParseIP("200.223.19.6"), Port: 6881},
			{IP: net.ParseIP("200.223.19.7"), Port: 6881},
			{IP: net.ParseIP("212.129.33.250"), Port: 6881},
			{IP: net.ParseIP("67.215.246.10"), Port: 6881},
			{IP: net.ParseIP("104.238.198.186"), Port: 6881},
		},

		knownNodes:     make(map[string]*nodeScore),
		queryQueue:     list.New(),
		announceTicker: time.NewTicker(100 * time.Millisecond),
		verbose:        true,
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
	go d.listen()
	go d.discoverNodes()
	go d.processQueries()
	go d.consumeAnnouncements()
	go d.queryRandomInfohashes()
	go d.cleanInactiveNodes()

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

func (d *dht) consumeAnnouncements() {
	logWithColor(LogLevelInfo, "⏳ 启动公告消费者")
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

				log.Printf("📨 发送公告: %s... (来源: %s)",
					ac.infohashHex[:8], ac.from.String())

				select {
				case d.chAnnouncement <- ac:
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
		if dict["q"] != "find_node" {
			logWithColor(LogLevelInfo, "收到查询请求 %s %s", from, dict["q"])
			d.onQuery(dict, from)
		}
	case "r":
		d.onResponse(dict, from)
	}
}

func (d *dht) onResponse(dict map[string]interface{}, from net.UDPAddr) {
	r, ok := dict["r"].(map[string]interface{})
	if !ok {
		return
	}

	if _, ok := r["nodes"]; ok {
		d.onFindNodeResponse(dict, from)
	} else if _, ok := r["token"]; ok {
		logWithColor(LogLevelInfo, "处理 get_peers 响应 %s", from)
		d.onGetPeersResponse(dict, from)
	} else if _, ok := r["id"]; ok {
		logWithColor(LogLevelInfo, "处理 ping 响应 %s", from)
	}
}

func (d *dht) onGetPeersResponse(dict map[string]interface{}, from net.UDPAddr) {
	r := dict["r"].(map[string]interface{})
	t, _ := dict["t"].(string)
	logWithColor(LogLevelInfo, "收到 get_peers 响应: 事务ID=%s 来源=%s", t[:min(4, len(t))], from.String())

	if values, ok := r["values"].([]interface{}); ok {
		for _, v := range values {
			if peer, ok := v.(string); ok && len(peer) == 6 {
				ip := net.IP(peer[:4])
				port := binary.BigEndian.Uint16([]byte(peer[4:6]))
				logWithColor(LogLevelInfo, "发现 peer: %s:%d (来自 %s)", ip, port, from.String())
			}
		}
	}

	if nodes, ok := r["nodes"].(string); ok && len(nodes) > 0 {
		d.onFindNodeResponse(dict, from)
	}

	if _, ok := r["token"].(string); ok {
		log.Println("收到 token，用于后续 announce_peer")
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

	if ac := d.summarize(dict, from); ac != nil {
		d.announcements.put(ac)

		// 已经有consumeAnnouncements协程处理，这里不再需要直接发送
		// select {
		// case d.chAnnouncement <- ac:
		// default:
		// }

		logWithColor(LogLevelInfo, "发现磁力链: %s (来源: %s)",
			ac.infohashHex, from.String())
	}
}

func (d *dht) send(dict map[string]interface{}, to net.UDPAddr) error {
	encoded := bencode.Encode(dict)
	_, err := d.conn.WriteToUDP(encoded, &to)
	return err
}

func (d *dht) queryRandomInfohashes() {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-d.die:
			return
		case <-ticker.C:
			d.nodesMutex.Lock() // 加锁
			if len(d.knownNodes) == 0 {
				d.nodesMutex.Unlock() // 如果提前返回，需要先解锁
				continue
			}

			var bestNode *net.UDPAddr
			maxScore := -1
			for _, ns := range d.knownNodes {
				if ns.score > maxScore {
					maxScore = ns.score
					bestNode = ns.addr
				}
			}
			d.nodesMutex.Unlock() // 解锁

			if bestNode == nil {
				continue
			}

			smartInfohash := d.generateSmartInfohash()
			// infohashHex := hex.EncodeToString(smartInfohash)

			if d.verbose {
				// log.Printf("🌐 查询智能磁力链: %s... (节点: %s)",
				// 	infohashHex[:8], bestNode.String())
			}
			d.getPeers(string(smartInfohash), *bestNode)
		}
	}
}

func (d *dht) generateSmartInfohash() []byte {
	randVal := rand.Intn(100)
	switch {
	case randVal < 40:
		return d.generateVideoInfohash()
	case randVal < 70:
		return d.generateGameInfohash()
	case randVal < 90:
		return d.generateImageInfohash()
	default:
		return randBytes(20)
	}
}

func (d *dht) generateVideoInfohash() []byte {
	videoPrefixes := [][]byte{
		{0x12, 0x34, 0x56},
		{0x78, 0x9a, 0xbc},
		{0xde, 0xf0, 0x12},
	}
	return d.applyPrefix(videoPrefixes[rand.Intn(len(videoPrefixes))])
}

func (d *dht) generateGameInfohash() []byte {
	gamePrefixes := [][]byte{
		{0x45, 0x67, 0x89},
		{0xab, 0xcd, 0xef},
		{0x01, 0x23, 0x45},
	}
	return d.applyPrefix(gamePrefixes[rand.Intn(len(gamePrefixes))])
}

func (d *dht) generateImageInfohash() []byte {
	imagePrefixes := [][]byte{
		{0x67, 0x89, 0xab},
		{0xcd, 0xef, 0x01},
		{0x23, 0x45, 0x67},
	}
	return d.applyPrefix(imagePrefixes[rand.Intn(len(imagePrefixes))])
}

func (d *dht) applyPrefix(prefix []byte) []byte {
	result := make([]byte, 20)
	copy(result, prefix)
	rand.Read(result[len(prefix):])
	return result
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
	// log.Printf("🌐 发送 find_node 到 %s, 目标: %s...", to.String(), hex.EncodeToString([]byte(target))[:8])
	d.send(query, to)
}

func (d *dht) onFindNodeResponse(dict map[string]interface{}, from net.UDPAddr) {
	r, ok := dict["r"].(map[string]interface{})
	if !ok {
		return
	}

	nodes, ok := r["nodes"].(string)
	if !ok || len(nodes)%26 != 0 {
		return
	}

	// log.Printf("✅ 收到来自 %s 的 find_node 响应，发现新节点", from.String())

	d.nodesMutex.Lock()         // 加锁
	defer d.nodesMutex.Unlock() // 推荐使用 defer 来确保解锁

	for i := 0; i < len(nodes); i += 26 {
		nodeData := nodes[i : i+26]
		id := nodeData[:20]
		ip := net.IP(nodeData[20:24])
		port := binary.BigEndian.Uint16([]byte(nodeData[24:26]))
		addr := &net.UDPAddr{IP: ip, Port: int(port)}

		if ns, exists := d.knownNodes[string(id)]; exists {
			ns.lastSeen = time.Now()
			ns.score = min(ns.score+1, 10)
		} else {
			d.knownNodes[string(id)] = &nodeScore{
				addr:     addr,
				score:    3,
				lastSeen: time.Now(),
			}
			if d.verbose {
				// log.Printf("➕ 发现新节点: %s (初始评分:3)", addr.String())
			}
		}
	}
}

func (d *dht) cleanInactiveNodes() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-d.die:
			return
		case <-ticker.C:
			d.nodesMutex.Lock() // 使用新的互斥锁
			count := 0
			now := time.Now()
			for id, ns := range d.knownNodes {
				if now.Sub(ns.lastSeen) > 15*time.Minute {
					delete(d.knownNodes, id)
					count++
				}
			}
			nodesCount := len(d.knownNodes)
			d.nodesMutex.Unlock() // 在打印日志前解锁

			if count > 0 && d.verbose {
				log.Printf("🧹 清理 %d 个不活跃节点，当前节点数: %d",
					count, nodesCount)
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (d *dht) discoverNodes() {
	// 启动时立即发送
	for _, addr := range d.bootstrapNodes {
		d.findNode(string(d.localID), *addr)
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.die:
			return
		case <-ticker.C:
			d.nodesMutex.Lock() // 加锁
			// 如果没有已知节点，则每隔3秒向所有 bootstrap 节点重新发送查询
			if len(d.knownNodes) == 0 {
				d.nodesMutex.Unlock() // 解锁
				logWithColor(LogLevelDebug, "🚨 没有已知节点，重新向所有 bootstrap 节点发送查询")
				for _, addr := range d.bootstrapNodes {
					d.findNode(string(d.localID), *addr)
				}
				continue
			}

			// 如果有已知节点，则向一个随机或高分节点发送查询
			var activeNode *net.UDPAddr
			var nodeIDs []string
			for id := range d.knownNodes {
				nodeIDs = append(nodeIDs, id)
			}
			if len(nodeIDs) > 0 {
				randomID := nodeIDs[rand.Intn(len(nodeIDs))]
				activeNode = d.knownNodes[randomID].addr
			}
			d.nodesMutex.Unlock() // 解锁

			if activeNode != nil {
				target := d.generateSmartInfohash()
				d.findNode(string(target), *activeNode)
				d.getPeersForPopularTorrents(*activeNode)
			}
		}
	}
}

func (d *dht) getPeersForPopularTorrents(to net.UDPAddr) {
	popularHashes := []string{
		"e2467cbf021192c241367b892230dc1e05c0580e",
		"5a8062c076fa85e8056456929059040c2a1e4c5d",
		"2081d049de3abf95b2338d4c2d0f6150e87e9d1e",
		"a88fda5954e89178c372716a6a78b8180ef4c1d3",
		"6a9759bffd5c0af65319979fb7832189f4f3c35d",
	}

	for _, hash := range popularHashes {
		// 为了避免过多的日志，这里只在verbose模式下打印
		if d.verbose {
			// log.Printf("🔍 查询流行磁力链: %s... (节点: %s)", hash[:8], to.String())
		}
		d.getPeers(hash, to)
	}
}

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
	d.send(query, to)
}

func (d *dht) processQueries() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.die:
			return
		case <-ticker.C:
			if hash, ok := d.dequeueQuery(); ok {
				if len(d.knownNodes) > 0 {
					var randomAddr *net.UDPAddr
					var nodeIDs []string
					for id := range d.knownNodes {
						nodeIDs = append(nodeIDs, id)
					}
					if len(nodeIDs) > 0 {
						randomID := nodeIDs[rand.Intn(len(nodeIDs))]
						randomAddr = d.knownNodes[randomID].addr
					}
					if randomAddr != nil {
						d.getPeers(hash, *randomAddr)
						log.Printf("🔍 查询磁力链: %s... (节点: %s)", hash[:8], randomAddr.String())
					}
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

func (d *dht) onPingQuery(dict map[string]interface{}, from net.UDPAddr) {
	tid, ok := dict["t"].(string)
	if !ok {
		return
	}

	reply := map[string]interface{}{
		"t": tid,
		"y": "r",
		"r": map[string]interface{}{
			"id": string(d.localID),
		},
	}
	d.send(reply, from)
	logWithColor(LogLevelWarn, "🔄 响应ping请求: %s", from.String())
}

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

	reply := map[string]interface{}{
		"t": tid,
		"y": "r",
		"r": map[string]interface{}{
			"id":    string(d.localID),
			"nodes": "",
		},
	}
	d.send(reply, from)
	logWithColor(LogLevelWarn, "📥 响应find_node请求: %s", from.String())
}

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
	logWithColor(LogLevelWarn, "📥 响应get_peers请求: %s (%s)", from.String(), infohash[:8])
}
