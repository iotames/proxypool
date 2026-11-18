// Package config 管理 proxypool 的运行时配置。
// 使用 easyconf 实现三级配置覆盖机制：
//  1. .env 文件（持久化存储，优先级最低）
//  2. 系统环境变量
//  3. 命令行参数（优先级最高）
//
// 使用方式：
//
//	cfg := config.Load()
//	fmt.Println(cfg.Port)
//	cfg.Save() // 将内存中的配置写回 .env 文件
package config

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/iotames/easyconf"
)

// Snapshot 配置在某一时刻的一致性拷贝。
// 运行时读取配置一律使用 Snapshot，避免与 Web 修改并发读写同一份字段。
type Snapshot struct {
	Port           int
	ConfPath       string
	Fixed          bool
	Fast           bool
	GoogleTimeout  int
	LatencyMax     int
	TestURL        string
	HealthInterval int
	PoolMaxSize    int
	BindAddress    string
	StickySession  bool
	SessionTTL     int
}

// Conf 存储所有可配置项。
type Conf struct {
	Port           int    // 代理服务监听端口（命令行参数 --port）
	ConfPath       string // Clash 配置文件路径（命令行参数 --conf）
	Fixed          bool   // 固定代理模式（命令行参数 --fixed），启用后始终使用测速最快的节点，不随机分配，不自动测速
	Fast           bool   // 快速模式（命令行参数 --fast），固定使用最快节点，但按 HEALTH_INTERVAL 重测并在节点失效时自动切换
	GoogleTimeout  int    // 谷歌连通性测试超时（毫秒）
	LatencyMax     int    // 延迟上限（毫秒），<=0 表示连通即可用、不按延迟淘汰
	TestURL        string // 连通性测试目标 URL
	HealthInterval int    // 健康检查间隔（秒）
	PoolMaxSize    int    // 代理池最大节点数（0 表示不限制）
	BindAddress    string // 代理服务绑定 IP
	StickySession  bool   // 粘性会话：按 Proxy-Authorization 用户名绑定节点，保证同一会话出口 IP 稳定
	SessionTTL     int    // 粘性会话空闲过期时间（秒），0 表示不过期

	mu sync.RWMutex // 保护运行期可被 Web API 修改的字段

	// 内部引用，用于保存配置到 .env 文件
	cf *easyconf.Conf
}

// PrintConfig 以表格形式打印当前配置项。
func (c *Conf) PrintConfig() {
	if c.cf == nil {
		log.Println("配置未加载")
		return
	}
	log.Println("----------------------------------------")
	log.Println(fmt.Sprintf("%-20s %-30s %-40s", "配置项", "配置值", "配置说明"))
	log.Println(fmt.Sprintf("%-20s %-30s %-40s", "--------", "--------", "--------"))
	for _, item := range c.cf.GetItems() {
		title := item.Title
		if len(item.Usage) > 0 {
			title = item.Usage[0]
		}
		log.Println(fmt.Sprintf("%-20s %-30s %-40s", item.Name, item.GetValue(), title))
	}
	log.Println("----------------------------------------")
}

// Load 加载配置，返回填充好的 Conf 实例。
// 配置优先级：命令行参数 > 系统环境变量 > .env 文件 > default.env 文件。
// confDir: .env 和 default.env 文件所在目录（通常为 main/）。
// parseFlags: 是否从命令行参数解析（测试时应传入 false）。
func Load(confDir string, parseFlags bool) (*Conf, error) {
	c := &Conf{}

	// 使用 easyconf，定义配置项
	cf := easyconf.NewConf(confDir+"/.env", confDir+"/default.env")

	// 命令行参数（同时也是 env 配置项）
	cf.IntVar(&c.Port, "port", 1080, "代理服务监听端口")
	cf.StringVar(&c.ConfPath, "conf", "clash.yaml", "Clash 配置文件路径")
	cf.BoolVar(&c.Fixed, "fixed", false, "固定代理模式(仅CLI)", "启用后始终使用测速最快的节点，不随机分配，不自动测速（仅命令行模式有效）")
	cf.BoolVar(&c.Fast, "fast", false, "快速模式(仅CLI)", "固定使用测速最快的节点，但按 HEALTH_INTERVAL 重测，节点掉出可用池时自动切换到最快节点（仅命令行模式有效）")

	// 环境变量配置项
	cf.IntVar(&c.GoogleTimeout, "GOOGLE_TIMEOUT", 300, "谷歌连通性测试超时(毫秒)", "连接/读取超时；不再作为延迟淘汰阈值")
	cf.IntVar(&c.LatencyMax, "LATENCY_MAX", 0, "节点延迟上限(毫秒)", "0 表示连通即可用、不按延迟淘汰；>0 时延迟超过该上限才判为不可用")
	cf.StringVar(&c.TestURL, "GOOGLE_TEST_URL", "http://www.google.com", "连通性测试目标URL", "http/https 均可；https 会先完成 TLS 握手再探测，延迟含握手耗时")
	cf.IntVar(&c.HealthInterval, "HEALTH_INTERVAL", 60, "健康检查间隔(秒)", "剔除节点后重测的时间间隔")
	cf.IntVar(&c.PoolMaxSize, "POOL_MAX_SIZE", 0, "代理池最大节点数", "0 表示不限制")
	cf.StringVar(&c.BindAddress, "BIND_ADDRESS", "127.0.0.1", "代理服务绑定IP地址")
	cf.BoolVar(&c.StickySession, "STICKY_SESSION", true, "粘性会话(出口IP固定)", "客户端以 http://会话名:@127.0.0.1:端口 形式请求时，同一会话固定复用同一节点")
	cf.IntVar(&c.SessionTTL, "SESSION_TTL", 600, "粘性会话空闲过期(秒)", "0 表示不过期；节点被移出代理池时会话立即失效并重新选路")

	// 解析配置（包括命令行参数）
	if err := cf.Parse(parseFlags); err != nil {
		return nil, err
	}

	c.cf = cf
	return c, nil
}

// Snapshot 返回当前配置的一致性拷贝。
func (c *Conf) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Snapshot{
		Port:           c.Port,
		ConfPath:       c.ConfPath,
		Fixed:          c.Fixed,
		Fast:           c.Fast,
		GoogleTimeout:  c.GoogleTimeout,
		LatencyMax:     c.LatencyMax,
		TestURL:        c.TestURL,
		HealthInterval: c.HealthInterval,
		PoolMaxSize:    c.PoolMaxSize,
		BindAddress:    c.BindAddress,
		StickySession:  c.StickySession,
		SessionTTL:     c.SessionTTL,
	}
}

// Apply 校验并应用一组运行时配置更新（键为环境变量名）。
// 全部校验通过后才原子提交，避免半更新状态；未知键忽略。
func (c *Conf) Apply(updates map[string]interface{}) error {
	snap := c.Snapshot()
	for key, v := range updates {
		switch key {
		case "GOOGLE_TIMEOUT":
			n, err := intValue(key, v, 1)
			if err != nil {
				return err
			}
			snap.GoogleTimeout = n
		case "LATENCY_MAX":
			n, err := intValue(key, v, 0)
			if err != nil {
				return err
			}
			snap.LatencyMax = n
		case "GOOGLE_TEST_URL":
			s, err := stringValue(key, v)
			if err != nil {
				return err
			}
			snap.TestURL = s
		case "HEALTH_INTERVAL":
			n, err := intValue(key, v, 1)
			if err != nil {
				return err
			}
			snap.HealthInterval = n
		case "POOL_MAX_SIZE":
			n, err := intValue(key, v, 0)
			if err != nil {
				return err
			}
			snap.PoolMaxSize = n
		case "BIND_ADDRESS":
			s, err := stringValue(key, v)
			if err != nil {
				return err
			}
			snap.BindAddress = s
		case "PORT":
			n, err := intValue(key, v, 1)
			if err != nil {
				return err
			}
			if n > 65535 {
				return fmt.Errorf("PORT 必须在 1-65535 之间")
			}
			snap.Port = n
		case "STICKY_SESSION":
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf("STICKY_SESSION 必须是布尔值")
			}
			snap.StickySession = b
		case "SESSION_TTL":
			n, err := intValue(key, v, 0)
			if err != nil {
				return err
			}
			snap.SessionTTL = n
		case "CONF_PATH":
			s, err := stringValue(key, v)
			if err != nil {
				return err
			}
			snap.ConfPath = s
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.Port = snap.Port
	c.ConfPath = snap.ConfPath
	c.Fixed = snap.Fixed
	c.Fast = snap.Fast
	c.GoogleTimeout = snap.GoogleTimeout
	c.LatencyMax = snap.LatencyMax
	c.TestURL = snap.TestURL
	c.HealthInterval = snap.HealthInterval
	c.PoolMaxSize = snap.PoolMaxSize
	c.BindAddress = snap.BindAddress
	c.StickySession = snap.StickySession
	c.SessionTTL = snap.SessionTTL
	return nil
}

// intValue 将 JSON 数字转换为整数并校验下限。
func intValue(key string, v interface{}, min int) (int, error) {
	f, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("%s 必须是数字", key)
	}
	n := int(f)
	if float64(n) != f {
		return 0, fmt.Errorf("%s 必须是整数", key)
	}
	if n < min {
		return 0, fmt.Errorf("%s 不能小于 %d", key, min)
	}
	return n, nil
}

// stringValue 将 JSON 字符串取出并去除空白，禁止空值。
func stringValue(key string, v interface{}) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s 必须是字符串", key)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%s 不能为空", key)
	}
	return s, nil
}

// Save 将当前配置保存到 .env 文件。
func (c *Conf) Save() error {
	if c.cf == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cf.UpdateFile("")
}
