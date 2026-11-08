# proxypool — 隧道代理工具

从 Clash 配置中提取代理节点，自动构建代理池，在本地暴露一个固定端口作为隧道代理，随机分配出口 IP，规避反爬虫策略。

## 编译

推荐使用 `make`（Windows 需安装 [Git for Windows](https://git-scm.com/downloads)，Makefile 会自动定位其 bash）：

```bash
go mod tidy

# 构建（自动识别平台，输出 proxypool 或 proxypool.exe）
make build

# 构建并启动
make run

# 清理产物
make clean
```

也可直接用 go 命令构建：

```bash
# 构建
cd main

# For Linux,Mac
go build -o proxypool .

# For Windows
go build -o proxypool.exe .
```

## 两种运行模式

### 命令行模式（CLI）

带 `--port` 和 `--conf` 参数启动，直接开启隧道代理：

```bash
# 构建
cd main

# 运行
proxypool.exe --port=1080 --conf=clash.yaml
```

也可直接执行快速启动脚本（需先 `make build`）：

- Windows：`main/run.bat`
- Linux/Mac：`sh main/run.sh`（或先 `chmod +x main/run.sh` 后直接执行）

启动后 `http://127.0.0.1:1080` 即为隧道代理入口，爬虫直接使用此地址即可随机切换出口 IP。

### Web 控制台模式

不带任何参数启动，打开浏览器操作：

```bash
# 运行（Web 控制台）
proxypool.exe
```

打开 `http://127.0.0.1:1081` 即可看到管理界面：
- 编辑配置后点击「启动隧道代理」
- 实时查看节点测试进度
- 查看代理池概况和节点列表（总节点包含不可用节点，可用数仅统计延迟达标的节点）
- 运行中可点击「立即巡检」，手动重测全部节点并重排代理池，无需重启
- 支持停止/重新启动；停止会释放代理端口并停止后台巡检，启动/停止操作可重复执行

配置加载流程：

```bash
# 启动 Web 模式
  config.Load(confDir, false)
    ├─ easyconf.NewConf("./.env", "./default.env") 
    ├─ Parse(false)
    │   ├─ SetValuesByEnvFile("default.env")   ← 读取 default.env
    │   ├─ SetValuesByEnvFile(".env")            ← 读取 .env（覆盖前者）
    │   └─ SetValuesByEnv()                      ← 系统环境变量（覆盖文件）
    └─ 返回 *Conf{Port, ConfPath, GoogleTimeout, ...}

# 用户点击「启动隧道代理」
   前端 startProxy()
      └─ saveConfig()
           └─ PUT /api/config {GOOGLE_TIMEOUT:300, PORT:1080, ...}
                ├─ 更新 s.cfg.Port, s.cfg.GoogleTimeout …  (内存)
                └─ s.cfg.Save() → UpdateFile(".env")        (持久化)
      └─ POST /api/start
           └─ startProxy(confDir)
                └─ goroutine → runProxy(confDir, s.cfg, ...)
                     ├─ clash.ParseFile(s.cfg.ConfPath)       ← 用新配置
                     ├─ node.TestNodes(..., s.cfg.GoogleTimeout)
                     ├─ proxy.NewServer("IP:PORT", ...)
                     └─ health.NewChecker(..., s.cfg.HealthInterval)
```


## 命令行参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--port` | int | 1080 | 代理端口 |
| `--conf` | string | clash.yaml | Clash 配置文件路径 |
| `--fixed` | bool | false | 固定模式：始终使用启动时测速最快的节点，不随机分配，不再自动测速 |
| `--fast` | bool | false | 快速模式：固定使用最快节点，但按 `HEALTH_INTERVAL` 定时重测，节点失效或掉出可用池时自动切换到最快节点 |

`--fixed` 与 `--fast` 互斥，不能同时使用。

### 节点选取模式

| 模式 | 启动方式 | 出口 IP | 健康检查 |
|------|----------|---------|----------|
| 随机模式 | 默认 | 每个连接随机变化 | 定时重测不可用节点，恢复后回池 |
| 固定模式 | `--fixed` | 恒定（启动时最快节点） | 不启动，节点失效后不会自动恢复 |
| 快速模式 | `--fast` | 恒定，仅在当前节点失效时切换 | 定时全量重测；当前节点仍可用则保持不变，失效后切换到最快节点 |

快速模式适用于需要稳定出口 IP（避免频繁触发网站验证码）、同时希望节点故障能自动恢复的场景。节点是否"可用"以连通性为准（能否经隧道成功返回响应）；`GOOGLE_TIMEOUT` 仅作连接/读取超时，不再按延迟淘汰；如需剔除慢节点用 `LATENCY_MAX`（默认 0=不淘汰）。当前节点真正连不通时才会移出可用池并自动切换。

## 环境变量配置

可通过 `.env` 文件（位于 `main/` 目录）或系统环境变量配置。配置优先级：**命令行参数 > 系统环境变量 > .env 文件**。

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `GOOGLE_TIMEOUT` | 300 | 连通性测试连接/读取超时（毫秒）；不再用于按延迟淘汰节点 |
| `LATENCY_MAX` | 0 | 节点延迟上限（毫秒）：0 表示连通即可用、不按延迟淘汰；>0 时总延迟超过该上限才标记为不可用 |
| `HEALTH_INTERVAL` | 60 | 健康检查间隔（秒） |
| `POOL_MAX_SIZE` | 0 | 代理池最大节点数，0 表示不限制 |
| `BIND_ADDRESS` | 127.0.0.1 | 代理服务绑定 IP |


## 工作原理

1. **读取 Clash 配置** — 解析 YAML 中的 `proxies`（ss + trojan）和 `proxy-providers`；自动忽略订阅中"剩余流量 / 距离下次重置 / 套餐到期"等信息展示伪节点
2. **测试节点** — 并发测试所有节点的 Google 连通性；能成功返回响应即视为可用（`LATENCY_MAX>0` 时才对延迟超上限的节点判不可用）；节点状态与延迟在测速与展示间原子读写，杜绝"延迟正常却显示不可用"的撕裂视图
3. **构建代理池** — 仅可用节点按延迟排序，每个请求从池中随机选取一个节点转发；固定/快速模式下始终使用当前钉住的最快节点
4. **隧道代理** — 支持 HTTP CONNECT、普通 HTTP、SOCKS5 三种协议自动识别
5. **健康检查** — 定时重试不可用节点，恢复后自动重新加入代理池；快速模式还会定时重测全部节点并在当前节点失效时切换最快节点

## 支持协议

- **HTTP CONNECT** — HTTPS 网站
- **普通 HTTP 代理** — HTTP 网站
- **SOCKS5** — 支持 SOCKS5 协议的应用

## 项目说明

- 配置文件 `.env` 和 `default.env` 由程序自动生成在 `main/` 目录
- Clash 配置文件为只读，程序不会修改它
- 只支持 `ss` 和 `trojan` 两种节点类型
