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

	"github.com/iotames/easyconf"
)

// Conf 存储所有可配置项。
type Conf struct {
	Port           int    // 代理服务监听端口（命令行参数 --port）
	ConfPath       string // Clash 配置文件路径（命令行参数 --conf）
	GoogleTimeout  int    // 谷歌连通性测试超时（毫秒）
	TestURL        string // 连通性测试目标 URL
	HealthInterval int    // 健康检查间隔（秒）
	PoolMaxSize    int    // 代理池最大节点数（0 表示不限制）
	BindAddress    string // 代理服务绑定 IP

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

	// 环境变量配置项
	cf.IntVar(&c.GoogleTimeout, "GOOGLE_TIMEOUT", 230, "谷歌连通性测试超时(毫秒)", "节点延迟阈值，超过此值的节点将被剔除")
	cf.StringVar(&c.TestURL, "GOOGLE_TEST_URL", "https://www.google.com", "连通性测试目标URL")
	cf.IntVar(&c.HealthInterval, "HEALTH_INTERVAL", 60, "健康检查间隔(秒)", "剔除节点后重测的时间间隔")
	cf.IntVar(&c.PoolMaxSize, "POOL_MAX_SIZE", 0, "代理池最大节点数", "0 表示不限制")
	cf.StringVar(&c.BindAddress, "BIND_ADDRESS", "127.0.0.1", "代理服务绑定IP地址")

	// 解析配置（包括命令行参数）
	if err := cf.Parse(parseFlags); err != nil {
		return nil, err
	}

	c.cf = cf
	return c, nil
}

// Save 将当前配置保存到 .env 文件。
func (c *Conf) Save() error {
	if c.cf == nil {
		return nil
	}
	return c.cf.UpdateFile("")
}
