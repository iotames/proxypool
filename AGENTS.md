# proxypool — 项目指南

## go.mod replace 指令规则

`go.mod` 中的 `replace github.com/iotames/easyconf => ./easyconf`：

- **开发阶段**：可取消注释，使用本地 `easyconf/` 目录
- **提交前**：必须重新注释为 `// replace github.com/iotames/easyconf => ./easyconf`

**理由**：`easyconf/` 是第三方依赖，不纳入版本管理。CI/CD 工作流会从远程自动下载 `github.com/iotames/easyconf`。

### 版本号同步

`require` 中的版本号（当前 `v1.2.2`）仅在 `replace` 被注释时生效，供 CI/CD 使用。如远程发布新版，需执行：

1. 注释 `replace` 行
2. `go mod tidy` 更新版本和 `go.sum`
3. 重新取消注释 `replace` 恢复本地开发

## 构建命令

推荐使用 `make`（Windows 需安装 Git for Windows，Makefile 会自动定位其 bash；`make build` 自动识别平台并注入版本号与构建时间）：

```bash
make build    # 构建（输出 proxypool 或 proxypool.exe）
make run      # 构建并启动
make clean    # 清理产物
```

也可直接用 go 命令：

```bash
cd main

# For Linux,Mac
go build -o proxypool .

# For Windows
go build -o proxypool.exe .
```


## 排查与下结论纪律

1. **先证实，再断言**：给出“根因/结论”前，必须已读全相关代码并握有可复现证据；明确区分「假设」与「已确认」。未经核实的猜测只能以“疑似/待验证”表述，禁止用自信措辞包装猜测。
2. **结构性问题看代码，别反推参数**：并发、共享可变状态、锁缺失、生命周期这类缺陷，先读读写双方的代码定性；不要先甩锅给某个配置/阈值“设得不合适”。
3. **错了当轮改判**：一旦证据推翻先前说法，立即更正并说明原因，不在旧结论上找补、不叠加辩护。

> **Why**：凭表面参数猜根因、被反驳后反复摇摆，会把修复引向错误方向、耗尽排查轮次，并摧毁用户对你判断的信任——比 bug 本身更伤。诊断的价值在“结论可靠”，不在“最先说出口”。

**反面案例（本仓库 proxypool，2026-11）**：现象是“节点延迟明明远低于阈值，却被标成不可用”——数据自相矛盾，一眼即假。当时我**没有先读**并发测速与快照读取的代码，武断归因为“`GOOGLE_TIMEOUT` 阈值太小”；被反驳后又来回改口，耗费多轮才靠**能证伪的证据**确认：真因是并发测速 goroutine 与 Web 快照读取**对同一共享状态字段无锁读写**造成的状态/延迟错配（数据竞争）——串行触发重测读不到矛盾、并发常态读才出现矛盾。低级、可耻。下次同类问题一律：**先读并发读写代码，先拿到能证伪的证据，再开口下结论。**

