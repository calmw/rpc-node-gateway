package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Redis       RedisConfig       `yaml:"redis"`
	Plans       map[string]Plan   `yaml:"plans"`
	Tokens      []TokenConfig     `yaml:"tokens"`
	Chains      map[string]Chain  `yaml:"chains"`
	HealthCheck HealthCheckConfig `yaml:"health_check"`
	Billing     BillingConfig     `yaml:"billing"`
}

type ServerConfig struct {
	Addr         string        `yaml:"addr"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	// Domains 绑定域名列表（匹配请求 Host，忽略端口）。空表示不校验。
	Domains []string `yaml:"domains"`
}

type RedisConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type Plan struct {
	// Token+IP 维度限流
	TokenIPRateLimitPerSecond int `yaml:"token_ip_rate_limit_per_second"`
	TokenIPRateLimitBurst     int `yaml:"token_ip_rate_limit_burst"`
	// Token 全局（不分 IP）限流
	TokenRateLimitPerSecond int `yaml:"token_rate_limit_per_second"`
	TokenRateLimitBurst     int `yaml:"token_rate_limit_burst"`

	DailyQuota    int64    `yaml:"daily_quota"`
	DeniedMethods []string `yaml:"denied_methods"`
}

type TokenConfig struct {
	Key         string `yaml:"key"`
	Plan        string `yaml:"plan"`
	Name        string `yaml:"name"`
	Enabled     bool   `yaml:"enabled"`
	BillingFree bool   `yaml:"billing_free"` // true=免费不计费，仍限流
}

type Chain struct {
	Name  string       `yaml:"name"`
	Nodes []NodeConfig `yaml:"nodes"`
}

type NodeConfig struct {
	URL    string `yaml:"url"`
	Weight int    `yaml:"weight"`
}

type HealthCheckConfig struct {
	Interval           time.Duration `yaml:"interval"`
	Timeout            time.Duration `yaml:"timeout"`
	UnhealthyThreshold int           `yaml:"unhealthy_threshold"`
	HealthyThreshold   int           `yaml:"healthy_threshold"`
}

type BillingConfig struct {
	Publisher string `yaml:"publisher"`
	StreamKey string `yaml:"stream_key"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 30 * time.Second
	}
	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = 60 * time.Second
	}
	if c.HealthCheck.Interval == 0 {
		c.HealthCheck.Interval = 30 * time.Second
	}
	if c.HealthCheck.Timeout == 0 {
		c.HealthCheck.Timeout = 5 * time.Second
	}
	if c.HealthCheck.UnhealthyThreshold <= 0 {
		c.HealthCheck.UnhealthyThreshold = 3
	}
	if c.HealthCheck.HealthyThreshold <= 0 {
		c.HealthCheck.HealthyThreshold = 2
	}
	if c.Billing.Publisher == "" {
		c.Billing.Publisher = "log"
	}
	if c.Billing.StreamKey == "" {
		c.Billing.StreamKey = "rpc:billing:events"
	}
	for name, plan := range c.Plans {
		plan.TokenIPRateLimitBurst = defaultBurst(plan.TokenIPRateLimitPerSecond, plan.TokenIPRateLimitBurst)
		plan.TokenRateLimitBurst = defaultBurst(plan.TokenRateLimitPerSecond, plan.TokenRateLimitBurst)
		c.Plans[name] = plan
	}
	for chainID, chain := range c.Chains {
		for i := range chain.Nodes {
			if chain.Nodes[i].Weight <= 0 {
				chain.Nodes[i].Weight = 1
			}
		}
		c.Chains[chainID] = chain
	}
	// 规范化域名：小写、去端口
	normalized := make([]string, 0, len(c.Server.Domains))
	seen := make(map[string]struct{})
	for _, d := range c.Server.Domains {
		d = normalizeHost(d)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		normalized = append(normalized, d)
	}
	c.Server.Domains = normalized
}

func defaultBurst(rps, burst int) int {
	if burst > 0 {
		return burst
	}
	if rps > 0 {
		return rps
	}
	return 1
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return ""
	}
	// 去掉协议前缀（防误配）
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimSuffix(host, "/")
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}

// HostAllowed 判断请求 Host 是否在绑定域名内。domains 为空则放行。
func HostAllowed(domains []string, host string) bool {
	if len(domains) == 0 {
		return true
	}
	host = normalizeHost(host)
	for _, d := range domains {
		if d == host {
			return true
		}
	}
	return false
}

func (c *Config) validate() error {
	if len(c.Plans) == 0 {
		return fmt.Errorf("config: at least one plan is required")
	}
	if len(c.Tokens) == 0 {
		return fmt.Errorf("config: at least one token is required")
	}
	if len(c.Chains) == 0 {
		return fmt.Errorf("config: at least one chain is required")
	}
	for _, t := range c.Tokens {
		if t.Key == "" {
			return fmt.Errorf("config: token key is empty")
		}
		if strings.Contains(t.Key, "/") {
			return fmt.Errorf("config: token key %q must not contain '/'", t.Key)
		}
		if _, ok := c.Plans[t.Plan]; !ok {
			return fmt.Errorf("config: token %q references unknown plan %q", t.Key, t.Plan)
		}
	}
	for id, chain := range c.Chains {
		if len(chain.Nodes) == 0 {
			return fmt.Errorf("config: chain %q has no nodes", id)
		}
	}
	return nil
}
