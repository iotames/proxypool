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

```bash
# cd main && go build -o proxypool .
cd main && go build -o proxypool.exe .
```
