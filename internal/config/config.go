package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all configuration for the application.
type Config struct {
	Server        ServerConfig        `mapstructure:"server"`
	Docker        DockerConfig        `mapstructure:"docker"`
	OpenClaw      OpenClawConfig      `mapstructure:"openclaw"`
	Database      DatabaseConfig      `mapstructure:"database"`
	KnowledgeBase KnowledgeBaseConfig `mapstructure:"knowledge_base"`
}

// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Host string `mapstructure:"host"`
}

// DockerConfig holds Docker container management configuration.
type DockerConfig struct {
	MaxContainers       int    `mapstructure:"max_containers"`
	PortRangeStart      int    `mapstructure:"port_range_start"`
	PortRangeEnd        int    `mapstructure:"port_range_end"`
	IdleTimeoutMinutes  int    `mapstructure:"idle_timeout_minutes"`
	MemoryLimit         string `mapstructure:"memory_limit"`
	CPULimit            string `mapstructure:"cpu_limit"`
	NetworkName         string `mapstructure:"network_name"`
	OpenClawImage       string `mapstructure:"openclaw_image"`
	OpenClawHostDataDir string `mapstructure:"openclaw_host_data_dir"`
}

// OpenClawConfig holds OpenClaw integration configuration.
type OpenClawConfig struct {
	APIKey       string           `mapstructure:"api_key"`
	BaseURL      string           `mapstructure:"base_url"`
	ModelProvider string          `mapstructure:"model_provider"`
	ModelName    string           `mapstructure:"model_name"`
	ToolsProfile string           `mapstructure:"tools_profile"`
	WebSearch    *WebSearchConfig `mapstructure:"web_search"`
	ExaSearch    *ExaSearchConfig `mapstructure:"exa_search"`
}

// ExaSearchConfig holds Exa.ai search configuration via mcporter MCP server.
type ExaSearchConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	APIKey  string `mapstructure:"api_key"`
}

// WebSearchConfig holds web search tool configuration (tools.web.search in openclaw.json).
type WebSearchConfig struct {
	Enabled         bool              `mapstructure:"enabled"`
	MaxResults      int               `mapstructure:"max_results"`
	TimeoutSeconds  int               `mapstructure:"timeout_seconds"`
	CacheTTLMinutes int               `mapstructure:"cache_ttl_minutes"`
	Kimi            *KimiSearchConfig `mapstructure:"kimi"`
}

// KimiSearchConfig holds Kimi (Moonshot) web search provider configuration.
type KimiSearchConfig struct {
	APIKey string `mapstructure:"api_key"`
}

// DatabaseConfig holds database configuration.
type DatabaseConfig struct {
	Driver string `mapstructure:"driver"`
	DSN    string `mapstructure:"dsn"`
}

// KnowledgeBaseConfig holds shared knowledge base configuration.
type KnowledgeBaseConfig struct {
	HostDir        string `mapstructure:"host_dir"`
	ContainerMount string `mapstructure:"container_mount"`
}

// Load reads configuration from file and environment variables.
func Load(configPath string) (*Config, error) {
	v := viper.New()

	// Set default values
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("docker.max_containers", 20)
	v.SetDefault("docker.port_range_start", 9001)
	v.SetDefault("docker.port_range_end", 9100)
	v.SetDefault("docker.idle_timeout_minutes", 30)
	v.SetDefault("docker.memory_limit", "2g")
	v.SetDefault("docker.cpu_limit", "1.0")
	v.SetDefault("docker.network_name", "weclaw-net")
	v.SetDefault("docker.openclaw_image", "ghcr.io/openclaw/openclaw:latest")
	v.SetDefault("docker.openclaw_host_data_dir", "./data/weclaw-openclaw")
	v.SetDefault("openclaw.tools_profile", "full")
	v.SetDefault("knowledge_base.host_dir", "./data/shared-knowledge")
	v.SetDefault("knowledge_base.container_mount", "/home/node/shared-knowledge")
	v.SetDefault("database.driver", "sqlite")
	v.SetDefault("database.dsn", "./data/weclaw.db")

	// Read config file
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath("./configs")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	// Read environment variables with WECLAW_ prefix
	v.SetEnvPrefix("WECLAW")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &cfg, nil
}
