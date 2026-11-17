package proxy

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/iotames/proxypool/internal/node"
)

// SOCKS5 协议常量。
const (
	socksVersion5         = 0x05
	socksAuthNone         = 0x00
	socksCmdConnect       = 0x01
	socksAddrTypeIPv4     = 0x01
	socksAddrTypeDomain   = 0x03
	socksAddrTypeIPv6     = 0x04
	socksReplySuccess     = 0x00
	socksReplyFailure     = 0x01
	socksReplyNotAllowed  = 0x02
	socksReplyUnreachable = 0x03
)

// handleSOCKS5 处理 SOCKS5 代理请求。
//
// SOCKS5 协议流程：
//  1. 握手: 客户端发送 [0x05][nMethods][methods...]
//     服务器回复 [0x05][selectedMethod]
//  2. 请求: 客户端发送 [0x05][cmd][0x00][addrType][addr][port]
//     服务器回复 [0x05][reply][0x00][addrType][addr][port]
//  3. 数据传输
//
// 所有读取统一走 br，避免已缓冲字节丢失。
func handleSOCKS5(clientConn net.Conn, br *bufio.Reader, targetNode *node.Node, timeout int) {
	// === 第一步：握手 ===
	// 读取 SOCKS5 握手包
	handshake := make([]byte, 2)
	if _, err := io.ReadFull(br, handshake); err != nil {
		log.Printf("读取 SOCKS5 握手失败: %v\n", err)
		return
	}

	ver := handshake[0]
	nMethods := handshake[1]
	if ver != socksVersion5 {
		log.Printf("不支持的 SOCKS 版本: 0x%02x\n", ver)
		return
	}

	// 读取认证方法列表
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(br, methods); err != nil {
		log.Printf("读取 SOCKS5 认证方法失败: %v\n", err)
		return
	}

	// 选择无认证方式
	if _, err := clientConn.Write([]byte{socksVersion5, socksAuthNone}); err != nil {
		log.Printf("发送 SOCKS5 握手响应失败: %v\n", err)
		return
	}

	// === 第二步：请求 ===
	// 读取请求头: [ver][cmd][rsv][addrType]
	reqHeader := make([]byte, 4)
	if _, err := io.ReadFull(br, reqHeader); err != nil {
		log.Printf("读取 SOCKS5 请求头失败: %v\n", err)
		return
	}

	cmd := reqHeader[1]
	addrType := reqHeader[3]

	if cmd != socksCmdConnect {
		log.Printf("不支持的 SOCKS5 命令: 0x%02x\n", cmd)
		clientConn.Write([]byte{socksVersion5, socksReplyNotAllowed, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// 解析目标地址
	var targetHost string
	switch addrType {
	case socksAddrTypeIPv4:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(br, addr); err != nil {
			log.Printf("读取 IPv4 地址失败: %v\n", err)
			return
		}
		targetHost = net.IP(addr).String()

	case socksAddrTypeDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(br, lenBuf); err != nil {
			log.Printf("读取域名长度失败: %v\n", err)
			return
		}
		domainLen := int(lenBuf[0])
		domain := make([]byte, domainLen)
		if _, err := io.ReadFull(br, domain); err != nil {
			log.Printf("读取域名失败: %v\n", err)
			return
		}
		targetHost = string(domain)

	case socksAddrTypeIPv6:
		addr := make([]byte, 16)
		if _, err := io.ReadFull(br, addr); err != nil {
			log.Printf("读取 IPv6 地址失败: %v\n", err)
			return
		}
		targetHost = net.IP(addr).String()

	default:
		log.Printf("不支持的地址类型: 0x%02x\n", addrType)
		clientConn.Write([]byte{socksVersion5, socksReplyFailure, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// 解析端口
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(br, portBuf); err != nil {
		log.Printf("读取端口失败: %v\n", err)
		return
	}
	targetPort := binary.BigEndian.Uint16(portBuf)
	targetAddr := net.JoinHostPort(targetHost, fmt.Sprintf("%d", targetPort))

	// 通过节点连接到目标
	remoteConn, err := node.DialThroughNode(targetNode, targetAddr, timeout)
	if err != nil {
		log.Printf("通过节点(%s)连接目标(%s)失败: %v\n", targetNode.Name, targetAddr, err)
		clientConn.Write([]byte{socksVersion5, socksReplyUnreachable, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer remoteConn.Close()

	// 获取代理服务器的本地地址用于在响应中返回
	localAddr, ok := clientConn.LocalAddr().(*net.TCPAddr)
	if !ok {
		localAddr = &net.TCPAddr{}
	}

	// 构建成功响应
	response := buildSOCKS5Reply(socksReplySuccess, localAddr.IP, localAddr.Port)
	if _, err := clientConn.Write(response); err != nil {
		log.Printf("发送 SOCKS5 响应失败: %v\n", err)
		return
	}

	log.Printf("[SOCKS5] %s -> %s (via %s)\n", clientConn.RemoteAddr(), targetAddr, targetNode.Name)

	// 双向转发数据
	relay(clientConn, remoteConn)
}

// buildSOCKS5Reply 构建 SOCKS5 响应包。
func buildSOCKS5Reply(reply byte, bindIP net.IP, bindPort int) []byte {
	// 简化为使用 IPv4 地址类型
	ip := bindIP.To4()
	if ip == nil {
		ip = net.IPv4(0, 0, 0, 0)
	}

	resp := make([]byte, 10)
	resp[0] = socksVersion5
	resp[1] = reply
	resp[2] = 0x00 // RSV
	resp[3] = socksAddrTypeIPv4
	copy(resp[4:8], ip)
	resp[8] = byte(bindPort >> 8)
	resp[9] = byte(bindPort & 0xFF)
	return resp
}
