# trojan 连接问题排查总结

## 现象

`proxypool --port=1080 --conf=clash.yaml` 启动后，所有节点测试结果为不可用。

错误日志：
```
节点(🇭🇰 香港 04 Pro) 不可用: 节点(🇭🇰 香港 04 Pro)读取响应失败: read tcp ... wsarecv: An existing connection was forcibly closed by the remote host.
```

trojan 服务器接受了 TCP 连接，但发送密码或协议数据后立即断开连接。

## 根本原因

使用的 clash.yaml 中的 trojan 节点类型为 `trojan`，但实际服务器运行的是 **Trojan-Go** 协议，而非原始 Trojan 协议。两者的协议实现差异较大。

## 差异对比

| 差异点 | 原始 Trojan | Trojan-Go（Clash Verge 使用） |
|---|---|---|
| **密码格式** | `[password]\r\n` | `[SHA224(password)]\r\n` |
| **传输层** | 裸 TCP | **TLS**（SNI 使用配置中的 `sni` 字段） |
| **地址格式** | `[ATYPE][ADDR][PORT]\r\n` | `[CMD=0x01][ATYPE][ADDR][PORT]\r\n` |
| **ALPN** | 无 | `h2, http/1.1` |

### 详细说明

1. **SHA224 密码哈希**：Trojan-Go 发送的是密码的 SHA224 哈希值（十六进制字符串），而非明文密码。
   - 明文：`b7584391-bf9e-44e5-913f-64728698678a`
   - SHA224：`b5a18facd05b7270fb43f14c2a858ab6d8e6915203357deb11cf0b9e`

2. **TLS 传输**：Trojan-Go 强制使用 TLS 封装整个协议，SNI 使用配置文件中的 `sni` 字段（如 `g.alicdn.com`）。Clash 配置中 `skip-cert-verify: true` 和 `sni: g.alicdn.com` 表明了这一点。

3. **CMD 字节**：地址前多了一个 `0x01`（CONNECT 命令）字节。

4. **ALPN**：TLS 握手时需要协商 `h2` 或 `http/1.1`，否则服务器可能拒绝连接。

## 排查过程

1. **Go 代码调试**：发现所有 trojan 节点 TCP 连接成功但被服务器断连
2. **Python 对照测试**：Go 和 Python 行为一致，排除语言差异
3. **DNS 检查**：系统 DNS 和 DoH 返回相同 IP，排除 DNS 污染
4. **TLS 测试**：TLS 握手成功但协议仍失败（ALPN 未设置）
5. **密码哈希测试**：使用 SHA224 哈希替换明文密码后成功
6. **完整验证**：TLS + SHA224 + CMD=0x01 + ALPN 全部到位后，收到 `HTTP/1.1 200 OK`

## 修复内容

**文件**：`internal/node/dial.go`

- 重写 `dialTrojan()` 函数
- 新增 TLS 连接（`crypto/tls`），使用配置中的 `sni` 和 `skip-cert-verify`
- 新增 `sha224Hex()` 函数计算密码的 SHA224 哈希
- 地址格式增加 CMD=0x01 前缀
- 设置 TLS ALPN 为 `["h2", "http/1.1"]`
