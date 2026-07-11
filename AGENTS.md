# proxypool — 项目指南

## go.mod replace 指令规则

`go.mod` 中的 `replace github.com/iotames/easyconf => ./easyconf`：

- **开发阶段**：可取消注释，使用本地 `easyconf/` 目录
- **提交前**：必须重新注释为 `// replace github.com/iotames/easyconf => ./easyconf`

**理由**：`easyconf/` 是第三方依赖，不纳入版本管理。CI/CD 工作流会从远程自动下载 `github.com/iotames/easyconf`。

## 构建命令

```bash
# cd main && go build -o proxypool .
cd main && go build -o proxypool.exe .
```
