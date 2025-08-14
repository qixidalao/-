# torsniff - DHT网络磁力链爬虫

torsniff 是一个高效的DHT网络磁力链爬虫工具，能够自动发现和分类BT网络中的磁力链接，并将结果保存到本地JSON文件中。

## 功能特性

- 🌐 **DHT网络监听**：实时监听DHT网络中的announce_peer请求
- 🧩 **智能分类**：基于infohash前缀自动分类磁力链（视频/游戏/图片/软件/其他）
- 💾 **持久化存储**：将发现的磁力链接保存到JSON文件
- 📊 **实时统计**：显示爬虫运行状态和分类统计信息
- 🚀 **高性能设计**：并发处理DHT请求和磁力链存储

## 安装与使用

### 安装要求
- Go 1.13+

### 编译安装
```bash
git clone https://github.com/yourusername/torsniff.git
cd torsniff
go build -o torsniff
```

### 运行命令
```bash
./torsniff \
  --addr 0.0.0.0 \     # 监听地址 (默认: 0.0.0.0)
  --port 9001 \        # 监听端口 (默认: 9001)
  --friends 500 \      # 最大DHT节点数 (默认: 500)
  --dir ./magnets \    # 存储目录 (默认: ./magnets)
  --verbose true       # 显示详细日志 (默认: true)
```

## 分类规则

磁力链根据infohash前缀自动分类：

| 类别        | 前缀示例      |
|-------------|--------------|
| 视频        | 123456, 789abc |
| 游戏        | 456789, abcdef |
| 图片/软件   | 6789ab, cdef01 |
| 其他        | 不匹配以上前缀 |

## 存储格式

磁力链以JSON格式存储在`magnets.json`文件中：

```json
[
  {
    "infohash": "e2467cbf021192c241367b892230dc1e05c0580e",
    "magnet": "magnet:?xt=urn:btih:e2467cbf021192c241367b892230dc1e05c0580e&tr=udp://...",
    "discovered": "2025-08-14T12:34:56Z"
  }
]
```

## 运行截图

```
ℹ️ [INFO] === torsniff 启动 ===
ℹ️ [INFO] 监听地址: 0.0.0.0:9001
ℹ️ [INFO] 最大节点数: 500
ℹ️ [INFO] 存储目录: /path/to/magnets
ℹ️ [INFO] 详细日志: true
ℹ️ [INFO] ====================
ℹ️ [INFO] 🌱 正在初始化爬虫...
ℹ️ [INFO] 💾 磁力链存储初始化完成
ℹ️ [INFO] 🌐 DHT网络初始化完成
ℹ️ [INFO] 🚀 DHT网络已启动
ℹ️ [INFO] ====================================
ℹ️ [INFO] 🏁 磁力链爬虫已启动，开始爬取数据...
ℹ️ [INFO] ====================================
⚠️ [WARN] 发现新磁力链 [0001]: magnet:?xt=urn:btih:1234567890abcdef...
ℹ️ [INFO] 发现视频磁力链 [0001]: 1234567890abcdef...
...
📊 状态统计: 运行 1小时02分30秒 | 发现 100 个磁力链 | 节点数 350 | 队列 50
分类统计: 视频=40, 游戏=30, 图片/软件=20, 其他=10
```

## 项目结构

```
torsniff/
├── content_prefixes.go      # 内容分类前缀
├── dht.go                   # DHT网络核心实现
├── json_store.go            # JSON存储实现
├── main.go                  # 程序入口
├── meta.go                  # 元数据获取（通过TCP）
├── torsniff_core.go         # 磁力链接构建工具
├── types.go                 # 数据结构定义
└── util.go                  # 工具函数
```

## 技术细节

1. **DHT网络实现**：
   - 使用UDP协议监听DHT网络
   - 实现KRPC协议处理find_node/get_peers/announce_peer等请求
   - 节点管理和评分机制

2. **智能磁力链发现**：
   - 基于内容类型的infohash生成策略
   - 分类统计（视频40%/游戏30%/图片软件20%/其他10%）

3. **存储系统**：
   - 自动创建存储目录
   - 避免重复存储
   - 定期保存到JSON文件

## 性能优化

- 节点连接数限制（默认500）
- 不活跃节点自动清理
- 查询队列管理
- 并发处理机制

## 注意事项

1. 需要开放UDP端口（默认9001）
2. 存储目录需要写入权限
3. 初次运行需要等待节点发现过程（约1-2分钟）

## 许可证

MIT License