package config

// HomeConfig 存储来自 -home-jwt 的仅运行时 Home 控制平面设置。
type HomeConfig struct {
	Enabled                 bool          `yaml:"enabled" json:"enabled"`
	NodeID                  string        `yaml:"-" json:"-"`
	Host                    string        `yaml:"host" json:"-"`
	Port                    int           `yaml:"port" json:"-"`
	DisableClusterDiscovery bool          `yaml:"disable-cluster-discovery" json:"-"`
	TLS                     HomeTLSConfig `yaml:"tls" json:"-"`
}

// HomeTLSConfig 配置 home Redis 连接的客户端 TLS。
type HomeTLSConfig struct {
	Enable              bool   `yaml:"enable" json:"-"`
	ServerName          string `yaml:"server-name" json:"-"`
	InsecureSkipVerify  bool   `yaml:"insecure-skip-verify" json:"-"`
	CACert              string `yaml:"ca-cert" json:"-"`
	ClientCert          string `yaml:"-" json:"-"`
	ClientKey           string `yaml:"-" json:"-"`
	UseTargetServerName bool   `yaml:"-" json:"-"`
}

// NormalizeHomePort 确保从 Home 收到的 CPA 服务器端口有效，
// 在缺失或非正数时默认为 11016。
func NormalizeHomePort(port int) int {
	if port <= 0 {
		return 11016
	}
	return port
}
