# proxypool — 隧道代理工具

从 Clash 配置中提取代理节点，自动构建代理池，在本地暴露一个固定端口作为隧道代理，随机分配出口 IP，规避反爬虫策略。

## 两种运行模式

### 命令行模式（CLI）

带 `--port` 和 `--conf` 参数启动，直接开启隧道代理：

```bash
# 构建
cd main
go build -o proxypool.exe .

# 运行
proxypool.exe --port=1080 --conf=clash.yaml
```

启动后 `http://127.0.0.1:1080` 即为隧道代理入口，爬虫直接使用此地址即可随机切换出口 IP。

### Web 控制台模式

不带任何参数启动，打开浏览器操作：

```bash
# 构建
cd main
go build -o proxypool.exe .

# 运行（Web 控制台）
proxypool.exe
```

打开 `http://127.0.0.1:1081` 即可看到管理界面：
- 编辑配置后点击「启动隧道代理」
- 实时查看节点测试进度
- 查看代理池概况和节点列表
- 支持停止/重新启动

## 命令行参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--port` | int | 1080 | 代理端口 |
| `--conf` | string | clash.yaml | Clash 配置文件路径 |

## 环境变量配置

可通过 `.env` 文件（位于 `main/` 目录）或系统环境变量配置。配置优先级：**命令行参数 > 系统环境变量 > .env 文件**。

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `GOOGLE_TIMEOUT` | 230 | 节点延迟阈值（毫秒），超过此值的节点将被剔除 |
| `HEALTH_INTERVAL` | 60 | 健康检查间隔（秒） |
| `POOL_MAX_SIZE` | 0 | 代理池最大节点数，0 表示不限制 |
| `BIND_ADDRESS` | 127.0.0.1 | 代理服务绑定 IP |

## 工作原理

1. **读取 Clash 配置** — 解析 YAML 中的 `proxies`（ss + trojan）和 `proxy-providers`
2. **测试节点** — 并发测试所有节点的 Google 连通性，筛选延迟 ≤230ms 的节点
3. **构建代理池** — 节点按延迟排序，每个请求从池中随机选取一个节点转发
4. **隧道代理** — 支持 HTTP CONNECT、普通 HTTP、SOCKS5 三种协议自动识别
5. **健康检查** — 定时重试不可用节点，恢复后自动重新加入代理池

## 支持协议

- **HTTP CONNECT** — HTTPS 网站
- **普通 HTTP 代理** — HTTP 网站
- **SOCKS5** — 支持 SOCKS5 协议的应用

## 项目说明

- 配置文件 `.env` 和 `default.env` 由程序自动生成在 `main/` 目录
- Clash 配置文件为只读，程序不会修改它
- 只支持 `ss` 和 `trojan` 两种节点类型
