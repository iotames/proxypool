package node

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// ssCipherInfo 存储 SS 加密算法参数。
type ssCipherInfo struct {
	keyLen   int // 密钥长度
	saltLen  int // 盐值长度
	newAEAD  func(key []byte) (cipher.AEAD, error)
}

// 支持的 SS 加密算法。
var supportedCiphers = map[string]ssCipherInfo{
	"aes-256-gcm":            {32, 32, newAESGCM},
	"aes-128-gcm":            {16, 16, newAESGCM},
	"chacha20-ietf-poly1305": {32, 32, newChaChaAEAD},
	"chacha20-poly1305":      {32, 32, newChaChaAEAD},
}

func newAESGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func newChaChaAEAD(key []byte) (cipher.AEAD, error) {
	return chacha20poly1305.New(key)
}

// ssAEADConn 实现 shadowsocks AEAD 加密的 TCP 连接。
// 符合 SIP018 规范。
type ssAEADConn struct {
	net.Conn
	info ssCipherInfo

	encMu   sync.Mutex
	encAEAD cipher.AEAD
	encNonce [12]byte // 12字节 nonce，使用最后 8 字节作为计数器

	decMu   sync.Mutex
	decAEAD cipher.AEAD
	decNonce [12]byte

	readBuf  []byte // 解密的剩余数据缓冲
	writeBuf []byte // 加密写入缓冲
}

// newSSConn 创建并握手 SS AEAD 加密连接。
// 1. 从密码派生出主密钥
// 2. 生成随机盐值，计算会话密钥
// 3. 发送盐值 + 加密的目标地址
func newSSConn(conn net.Conn, cipherName, password, targetAddr string) (*ssAEADConn, error) {
	info, ok := supportedCiphers[cipherName]
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("不支持的 ss 加密方式: %s", cipherName)
	}

	// 派生主密钥（EVP_BytesToKey）
	masterKey := evpBytesToKey([]byte(password), info.keyLen)

	// 生成随机盐值
	salt := make([]byte, info.saltLen)
	if _, err := rand.Read(salt); err != nil {
		conn.Close()
		return nil, fmt.Errorf("生成 ss 盐值失败: %w", err)
	}

	// 派生会话密钥：HKDF-SHA1(masterKey, salt, "ss-subkey")
	hkdfReader := hkdf.New(sha1.New, masterKey, salt, []byte("ss-subkey"))
	sessionKey := make([]byte, info.keyLen)
	if _, err := io.ReadFull(hkdfReader, sessionKey); err != nil {
		conn.Close()
		return nil, fmt.Errorf("派生 ss 会话密钥失败: %w", err)
	}

	// 创建 AEAD 实例
	aead, err := info.newAEAD(sessionKey)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("创建 SS AEAD 失败: %w", err)
	}

	// 编码目标地址
	addrBytes := EncodeSocks5Addr(targetAddr)
	if addrBytes == nil {
		conn.Close()
		return nil, fmt.Errorf("编码目标地址失败: %s", targetAddr)
	}

	// nonce=0 加密目标地址
	var zeroNonce [12]byte
	encAddr := aead.Seal(nil, zeroNonce[:], addrBytes, nil)

	// 发送: salt + encAddr
	payload := make([]byte, len(salt)+len(encAddr))
	copy(payload, salt)
	copy(payload[len(salt):], encAddr)
	if _, err := conn.Write(payload); err != nil {
		conn.Close()
		return nil, fmt.Errorf("发送 SS 握手数据失败: %w", err)
	}

	sc := &ssAEADConn{
		Conn:    conn,
		info:    info,
		readBuf: make([]byte, 0, 4096),
	}
	sc.encAEAD = aead
	sc.decAEAD = aead
	// 握手使用了 nonce 0，数据从 nonce 1 开始
	binary.BigEndian.PutUint64(sc.encNonce[4:], 1)
	binary.BigEndian.PutUint64(sc.decNonce[4:], 1)

	return sc, nil
}

// Write 加密数据并写入底层连接。
// SS AEAD 流式加密：每个数据包最大 0x3FFF 字节。
func (c *ssAEADConn) Write(data []byte) (int, error) {
	c.encMu.Lock()
	defer c.encMu.Unlock()

	written := 0
	for len(data) > 0 {
		// 每个 chunk 最大 0x3FFF
		chunkSize := len(data)
		if chunkSize > 0x3FFF {
			chunkSize = 0x3FFF
		}
		chunk := data[:chunkSize]
		data = data[chunkSize:]

		// 加密长度字段 (2字节)
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(chunkSize))
		encLen := c.encAEAD.Seal(nil, c.encNonce[:], lenBuf[:], nil)
		incrementNonce(&c.encNonce)

		// 加密数据
		encData := c.encAEAD.Seal(nil, c.encNonce[:], chunk, nil)
		incrementNonce(&c.encNonce)

		// 写入
		if _, err := c.Conn.Write(encLen); err != nil {
			return written, err
		}
		if _, err := c.Conn.Write(encData); err != nil {
			return written, err
		}
		written += chunkSize
	}
	return written, nil
}

// Read 从底层连接读取并解密数据。
func (c *ssAEADConn) Read(data []byte) (int, error) {
	c.decMu.Lock()
	defer c.decMu.Unlock()

	// 如果有缓冲数据，优先返回
	if len(c.readBuf) > 0 {
		n := copy(data, c.readBuf)
		c.readBuf = c.readBuf[n:]
		return n, nil
	}

	// 读取加密的长度字段
	encLenSize := 2 + c.decAEAD.Overhead()
	encLen := make([]byte, encLenSize)
	if _, err := io.ReadFull(c.Conn, encLen); err != nil {
		return 0, err
	}

	// 解密长度
	lenPlain, err := c.decAEAD.Open(nil, c.decNonce[:], encLen, nil)
	if err != nil {
		return 0, fmt.Errorf("SS 解密长度字段失败: %w", err)
	}
	incrementNonce(&c.decNonce)

	chunkLen := binary.BigEndian.Uint16(lenPlain)
	if chunkLen > 0x3FFF {
		return 0, fmt.Errorf("SS 数据包长度异常: %d", chunkLen)
	}

	// 读取加密的数据
	encData := make([]byte, int(chunkLen)+c.decAEAD.Overhead())
	if _, err := io.ReadFull(c.Conn, encData); err != nil {
		return 0, err
	}

	// 解密数据
	plain, err := c.decAEAD.Open(nil, c.decNonce[:], encData, nil)
	if err != nil {
		return 0, fmt.Errorf("SS 解密数据失败: %w", err)
	}
	incrementNonce(&c.decNonce)

	n := copy(data, plain)
	if n < len(plain) {
		c.readBuf = append(c.readBuf[:0], plain[n:]...)
	}
	return n, nil
}

// SetDeadline 设置超时。
func (c *ssAEADConn) SetDeadline(t time.Time) error {
	return c.Conn.SetDeadline(t)
}

// SetReadDeadline 设置读取超时。
func (c *ssAEADConn) SetReadDeadline(t time.Time) error {
	return c.Conn.SetReadDeadline(t)
}

// SetWriteDeadline 设置写入超时。
func (c *ssAEADConn) SetWriteDeadline(t time.Time) error {
	return c.Conn.SetWriteDeadline(t)
}

// incrementNonce 将 12 字节 nonce 的最后 8 字节加 1。
func incrementNonce(nonce *[12]byte) {
	for i := 11; i >= 4; i-- {
		nonce[i]++
		if nonce[i] != 0 {
			break
		}
	}
}

// evpBytesToKey 实现 OpenSSL 的 EVP_BytesToKey 算法。
// 用于从密码派生 shadowsocks 主密钥。
func evpBytesToKey(password []byte, keyLen int) []byte {
	var digest []byte
	var result []byte
	for len(result) < keyLen {
		h := sha1.New()
		if len(digest) > 0 {
			h.Write(digest)
		}
		h.Write(password)
		digest = h.Sum(nil)
		result = append(result, digest...)
	}
	return result[:keyLen]
}
