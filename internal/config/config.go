package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Node struct {
		Name     string `mapstructure:"name"`
		DataDir  string `mapstructure:"data_dir"`
		MaxPeers int    `mapstructure:"max_peers"`
	} `mapstructure:"node"`

	Mesh struct {
		ListenAddresses   []string      `mapstructure:"listen_addresses"`
		Port              int           `mapstructure:"port"`
		EnableUnix        bool          `mapstructure:"enable_unix"`
		EnableTCP         bool          `mapstructure:"enable_tcp"`
		EnableMulticast   bool          `mapstructure:"enable_multicast"`
		EnableDHT         bool          `mapstructure:"enable_dht"`
		EnableRelay       bool          `mapstructure:"enable_relay"`
		EnablePunch       bool          `mapstructure:"enable_punch"`
		StaticPeers       []string      `mapstructure:"static_peers"`
		BootstrapPeers    []string      `mapstructure:"bootstrap_peers"`
		DialTimeout       time.Duration `mapstructure:"dial_timeout"`
		AcceptTimeout     time.Duration `mapstructure:"accept_timeout"`
		IdleTimeout       time.Duration `mapstructure:"idle_timeout"`
		MaxFanout         int           `mapstructure:"max_fanout"`
		EnableFanout      bool          `mapstructure:"enable_fanout"`
		HeartbeatInterval time.Duration `mapstructure:"heartbeat_interval"`
		ReconnectInterval time.Duration `mapstructure:"reconnect_interval"`
		MaxRetries        int           `mapstructure:"max_retries"`
	} `mapstructure:"mesh"`

	Storage struct {
		Path             string        `mapstructure:"path"`
		MaxBlobSize      int64         `mapstructure:"max_blob_size"`
		MaxTotalSize     int64         `mapstructure:"max_total_size"`
		Compression      string        `mapstructure:"compression"`
		CompressionLevel int           `mapstructure:"compression_level"`
		EnableDedup      bool          `mapstructure:"enable_dedup"`
		EnableVersioning bool          `mapstructure:"enable_versioning"`
		MaxVersions      int           `mapstructure:"max_versions"`
		GCInterval       time.Duration `mapstructure:"gc_interval"`
		MaxAge           time.Duration `mapstructure:"max_age"`
		EnableSync       bool          `mapstructure:"enable_sync"`
		SyncInterval     time.Duration `mapstructure:"sync_interval"`
	} `mapstructure:"storage"`

	Compute struct {
		MaxCPUPercent      float64       `mapstructure:"max_cpu_percent"`
		MaxMemoryMB        int           `mapstructure:"max_memory_mb"`
		TickTimeout        time.Duration `mapstructure:"tick_timeout"`
		CheckpointInterval int           `mapstructure:"checkpoint_interval"`
		EnableTracing      bool          `mapstructure:"enable_tracing"`
		EnableHotReload    bool          `mapstructure:"enable_hot_reload"`
		HotReloadInterval  time.Duration `mapstructure:"hot_reload_interval"`
	} `mapstructure:"compute"`

	WASM struct {
		Enabled         bool     `mapstructure:"enabled"`
		MaxFuel         uint64   `mapstructure:"max_fuel"`
		MemoryLimit     uint32   `mapstructure:"memory_limit"`
		EnableWASI      bool     `mapstructure:"enable_wasi"`
		AllowNetwork    bool     `mapstructure:"allow_network"`
		AllowFileSystem bool     `mapstructure:"allow_filesystem"`
		ModuleCacheSize int      `mapstructure:"module_cache_size"`
		ModulePaths     []string `mapstructure:"module_paths"`
		ModuleDir       string   `mapstructure:"module_dir"`
		AutoLoad        bool     `mapstructure:"auto_load"`
		PersistencePath string   `mapstructure:"persistence_path"`
	} `mapstructure:"wasm"`

	Relay struct {
		Enabled    bool          `mapstructure:"enabled"`
		URL        string        `mapstructure:"url"`
		Topic      string        `mapstructure:"topic"`
		CipherKey  string        `mapstructure:"cipher_key"`
		Interval   time.Duration `mapstructure:"interval"`
		BackoffMax time.Duration `mapstructure:"backoff_max"`
		Timeout    time.Duration `mapstructure:"timeout"`
	} `mapstructure:"relay"`

	Cron struct {
		Enabled         bool   `mapstructure:"enabled"`
		Timezone        string `mapstructure:"timezone"`
		MaxConcurrent   int    `mapstructure:"max_concurrent"`
		PersistencePath string `mapstructure:"persistence_path"`
	} `mapstructure:"cron"`

	Capability struct {
		Issuer           string        `mapstructure:"issuer"`
		DefaultTTL       time.Duration `mapstructure:"default_ttl"`
		MaxTTL           time.Duration `mapstructure:"max_ttl"`
		RotationInterval time.Duration `mapstructure:"rotation_interval"`
		RevocationCheck  bool          `mapstructure:"revocation_check"`
	} `mapstructure:"capability"`

	RPC struct {
		Enabled            bool          `mapstructure:"enabled"`
		ListenAddress      string        `mapstructure:"listen_address"`
		DefaultTimeout     time.Duration `mapstructure:"default_timeout"`
		MaxConcurrentCalls int           `mapstructure:"max_concurrent_calls"`
		EnableCompression  bool          `mapstructure:"enable_compression"`
		CompressionLevel   int           `mapstructure:"compression_level"`
		EnableMetrics      bool          `mapstructure:"enable_metrics"`
		EnableTracing      bool          `mapstructure:"enable_tracing"`
	} `mapstructure:"rpc"`

	Health struct {
		Enabled       bool   `mapstructure:"enabled"`
		ListenAddr    string `mapstructure:"listen_addr"`
		EnableMetrics bool   `mapstructure:"enable_metrics"`
		MetricsPath   string `mapstructure:"metrics_path"`
	} `mapstructure:"health"`

	Logging struct {
		Level      string `mapstructure:"level"`
		Format     string `mapstructure:"format"`
		OutputPath string `mapstructure:"output_path"`
		MaxSize    int    `mapstructure:"max_size"`
		MaxBackups int    `mapstructure:"max_backups"`
		MaxAge     int    `mapstructure:"max_age"`
		Compress   bool   `mapstructure:"compress"`
	} `mapstructure:"logging"`

	RateLimit struct {
		Enabled        bool    `mapstructure:"enabled"`
		RequestsPerSec float64 `mapstructure:"requests_per_sec"`
		Burst          int     `mapstructure:"burst"`
	} `mapstructure:"rate_limit"`

	CircuitBreaker struct {
		Enabled     bool          `mapstructure:"enabled"`
		Threshold   float64       `mapstructure:"threshold"`
		Timeout     time.Duration `mapstructure:"timeout"`
		MinRequests int           `mapstructure:"min_requests"`
	} `mapstructure:"circuit_breaker"`

	BlobSync struct {
		Enabled    bool          `mapstructure:"enabled"`
		Interval   time.Duration `mapstructure:"interval"`
		BatchSize  int           `mapstructure:"batch_size"`
		MaxRetries int           `mapstructure:"max_retries"`
	} `mapstructure:"blob_sync"`

	Bridge struct {
		AutoReconnect     bool          `mapstructure:"auto_reconnect"`
		ReconnectInterval time.Duration `mapstructure:"reconnect_interval"`
		MaxMessageSize    int           `mapstructure:"max_message_size"`
	} `mapstructure:"bridge"`

	Punch struct {
		Enabled     bool          `mapstructure:"enabled"`
		MaxRetries  int           `mapstructure:"max_retries"`
		BaseBackoff time.Duration `mapstructure:"base_backoff"`
		MaxBackoff  time.Duration `mapstructure:"max_backoff"`
		Multiplier  float64       `mapstructure:"multiplier"`
	} `mapstructure:"punch"`
}

var cfg *Config

func Load(configPath string) (*Config, error) {
	v := viper.New()
	v.SetConfigName("hivemind")
	v.SetConfigType("yaml")

	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.AddConfigPath(".")
		v.AddConfigPath("$HOME/.hivemind")
		v.AddConfigPath("/etc/hivemind")
	}

	// Environment variable overrides
	v.SetEnvPrefix("HIVEMIND")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Set defaults
	setDefaults(v)

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("config file error: %w", err)
		}
		// Config file not found, use defaults + env
	}

	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("config unmarshal error: %w", err)
	}

	// Validate
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config validation error: %w", err)
	}

	cfg = &c
	return &c, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("node.name", "")
	v.SetDefault("node.data_dir", ".hivemind")
	v.SetDefault("node.max_peers", 50)

	v.SetDefault("mesh.listen_addresses", []string{"/ip4/0.0.0.0/tcp/0", "/ip6/::/tcp/0"})
	v.SetDefault("mesh.port", 0)
	v.SetDefault("mesh.enable_unix", true)
	v.SetDefault("mesh.enable_tcp", true)
	v.SetDefault("mesh.enable_multicast", true)
	v.SetDefault("mesh.enable_dht", true)
	v.SetDefault("mesh.enable_relay", true)
	v.SetDefault("mesh.enable_punch", false)
	v.SetDefault("mesh.dial_timeout", "10s")
	v.SetDefault("mesh.accept_timeout", "5s")
	v.SetDefault("mesh.idle_timeout", "60s")
	v.SetDefault("mesh.max_fanout", 8)
	v.SetDefault("mesh.enable_fanout", true)
	v.SetDefault("mesh.heartbeat_interval", "10s")
	v.SetDefault("mesh.reconnect_interval", "30s")
	v.SetDefault("mesh.max_retries", 3)

	v.SetDefault("relay.enabled", true)
	v.SetDefault("relay.url", "")
	v.SetDefault("relay.topic", "hive-relay")
	v.SetDefault("relay.interval", "5s")
	v.SetDefault("relay.backoff_max", "5m")
	v.SetDefault("relay.timeout", "10s")
	v.SetDefault("relay.mqtt_enabled", true)
	v.SetDefault("relay.mqtt_broker", "tcp://broker.hivemq.com:1883")

	v.SetDefault("storage.path", ".hive_blobs")
	v.SetDefault("storage.max_blob_size", 100*1024*1024)
	v.SetDefault("storage.max_total_size", 10*1024*1024*1024)
	v.SetDefault("storage.compression", "zstd")
	v.SetDefault("storage.compression_level", 3)
	v.SetDefault("storage.enable_dedup", true)
	v.SetDefault("storage.enable_versioning", true)
	v.SetDefault("storage.max_versions", 10)
	v.SetDefault("storage.gc_interval", "1h")
	v.SetDefault("storage.max_age", "720h")
	v.SetDefault("storage.enable_sync", true)
	v.SetDefault("storage.sync_interval", "5m")

	v.SetDefault("compute.max_cpu_percent", 50.0)
	v.SetDefault("compute.max_memory_mb", 512)
	v.SetDefault("compute.tick_timeout", "2s")
	v.SetDefault("compute.checkpoint_interval", 100)
	v.SetDefault("compute.enable_tracing", true)
	v.SetDefault("compute.enable_hot_reload", false)
	v.SetDefault("compute.hot_reload_interval", "30s")

	v.SetDefault("relay.enabled", true)
	v.SetDefault("relay.url", "")
	v.SetDefault("relay.topic", "hive-relay")
	v.SetDefault("relay.cipher_key", "")
	v.SetDefault("relay.interval", "5s")
	v.SetDefault("relay.backoff_max", "5m")
	v.SetDefault("relay.timeout", "10s")
	v.SetDefault("relay.mqtt_enabled", true)
	v.SetDefault("relay.mqtt_broker", "tcp://broker.hivemq.com:1883")

	v.SetDefault("storage.path", ".hive_blobs")
	v.SetDefault("storage.max_blob_size", 100*1024*1024)
	v.SetDefault("storage.max_total_size", 10*1024*1024*1024)
	v.SetDefault("storage.compression", "zstd")
	v.SetDefault("storage.compression_level", 3)
	v.SetDefault("storage.enable_dedup", true)
	v.SetDefault("storage.enable_versioning", true)
	v.SetDefault("storage.max_versions", 10)
	v.SetDefault("storage.gc_interval", "1h")
	v.SetDefault("storage.max_age", "720h")
	v.SetDefault("storage.enable_sync", true)
	v.SetDefault("storage.sync_interval", "5m")

	v.SetDefault("compute.max_cpu_percent", 50.0)
	v.SetDefault("compute.max_memory_mb", 512)
	v.SetDefault("compute.tick_timeout", "2s")
	v.SetDefault("compute.checkpoint_interval", 100)
	v.SetDefault("compute.enable_tracing", true)
	v.SetDefault("compute.enable_hot_reload", false)
	v.SetDefault("compute.hot_reload_interval", "30s")

	v.SetDefault("wasm.enabled", false)
	v.SetDefault("wasm.module_dir", ".hivemind/wasm")
	v.SetDefault("wasm.auto_load", true)
	v.SetDefault("wasm.persistence_path", ".hivemind/wasm_cache")
	v.SetDefault("wasm.max_fuel", 10_000_000)
	v.SetDefault("wasm.memory_limit", 256)
	v.SetDefault("wasm.enable_wasi", false)
	v.SetDefault("wasm.allow_network", false)
	v.SetDefault("wasm.allow_filesystem", false)
	v.SetDefault("wasm.module_cache_size", 50)
	v.SetDefault("wasm.module_paths", []string{})

	v.SetDefault("relay.enabled", true)
	v.SetDefault("relay.url", "https://ntfy.sh/cerberus-hive-relay-99")
	v.SetDefault("relay.topic", "cerberus-hive-relay-99")
	v.SetDefault("relay.interval", "5s")
	v.SetDefault("relay.backoff_max", "5m")
	v.SetDefault("relay.timeout", "10s")

	v.SetDefault("cron.enabled", true)
	v.SetDefault("cron.timezone", "UTC")
	v.SetDefault("cron.max_concurrent", 10)
	v.SetDefault("cron.persistence_path", ".hivemind/cron")

	v.SetDefault("capability.issuer", "hivemind")
	v.SetDefault("capability.default_ttl", "1h")
	v.SetDefault("capability.max_ttl", "24h")
	v.SetDefault("capability.rotation_interval", "90d")
	v.SetDefault("capability.revocation_check", true)

	v.SetDefault("rpc.enabled", true)
	v.SetDefault("rpc.listen_address", ":0")
	v.SetDefault("rpc.default_timeout", "30s")
	v.SetDefault("rpc.max_concurrent_calls", 100)
	v.SetDefault("rpc.enable_compression", false)
	v.SetDefault("rpc.compression_level", 3)
	v.SetDefault("rpc.enable_metrics", true)
	v.SetDefault("rpc.enable_tracing", true)

	v.SetDefault("health.enabled", true)
	v.SetDefault("health.listen_addr", ":8080")
	v.SetDefault("health.enable_metrics", true)
	v.SetDefault("health.metrics_path", "/metrics")

	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "json")
	v.SetDefault("logging.output_path", "stdout")
	v.SetDefault("logging.max_size", 100)
	v.SetDefault("logging.max_backups", 7)
	v.SetDefault("logging.max_age", 30)
	v.SetDefault("logging.compress", true)

	v.SetDefault("rate_limit.enabled", true)
	v.SetDefault("rate_limit.requests_per_sec", 100)
	v.SetDefault("rate_limit.burst", 20)

	v.SetDefault("circuit_breaker.enabled", true)
	v.SetDefault("circuit_breaker.threshold", 0.5)
	v.SetDefault("circuit_breaker.timeout", "30s")
	v.SetDefault("circuit_breaker.min_requests", 10)

	v.SetDefault("blob_sync.enabled", true)
	v.SetDefault("blob_sync.interval", "5m")
	v.SetDefault("blob_sync.batch_size", 100)
	v.SetDefault("blob_sync.max_retries", 3)

	v.SetDefault("wasm.enabled", false)
	v.SetDefault("wasm.module_dir", ".hivemind/wasm")
	v.SetDefault("wasm.auto_load", true)
	v.SetDefault("wasm.persistence_path", ".hivemind/wasm_cache")
	v.SetDefault("wasm.module_cache_size", 50)
	v.SetDefault("wasm.module_paths", []string{})

	v.SetDefault("bridge.auto_reconnect", true)
	v.SetDefault("bridge.reconnect_interval", "30s")
	v.SetDefault("bridge.max_message_size", 256*1024)

	v.SetDefault("punch.enabled", false)
	v.SetDefault("punch.max_retries", 3)
	v.SetDefault("punch.base_backoff", "2s")
	v.SetDefault("punch.max_backoff", "60s")
	v.SetDefault("punch.multiplier", 2.0)
}

func (c *Config) Validate() error {
	if c.Node.MaxPeers <= 0 {
		return fmt.Errorf("node.max_peers must be > 0")
	}
	if c.Mesh.Port < 0 || c.Mesh.Port > 65535 {
		return fmt.Errorf("mesh.port must be 0-65535")
	}
	if c.Storage.MaxBlobSize <= 0 {
		return fmt.Errorf("storage.max_blob_size must be > 0")
	}
	if c.Compute.MaxCPUPercent <= 0 || c.Compute.MaxCPUPercent > 100 {
		return fmt.Errorf("compute.max_cpu_percent must be 1-100")
	}
	if c.Health.Enabled && c.Health.ListenAddr == "" {
		return fmt.Errorf("health.listen_addr required when health enabled")
	}
	if c.RateLimit.Enabled && c.RateLimit.RequestsPerSec <= 0 {
		return fmt.Errorf("rate_limit.requests_per_sec must be > 0")
	}
	return nil
}

func (c *Config) DataDir() string {
	if c.Node.DataDir == "" {
		return ".hivemind"
	}
	return c.Node.DataDir
}

func (c *Config) RelayKeyPath() string {
	return filepath.Join(c.DataDir(), ".relaykey")
}

func (c *Config) SoulDir() string {
	return filepath.Join(c.DataDir(), "souls")
}

func (c *Config) BlobPath() string {
	if c.Storage.Path == "" {
		return filepath.Join(c.DataDir(), "blobs")
	}
	return c.Storage.Path
}

func (c *Config) WASMModuleDir() string {
	if c.WASM.ModuleDir == "" {
		return filepath.Join(c.DataDir(), "wasm")
	}
	return c.WASM.ModuleDir
}

func (c *Config) WASMPersistencePath() string {
	if c.WASM.PersistencePath == "" {
		return filepath.Join(c.DataDir(), "wasm_cache")
	}
	return c.WASM.PersistencePath
}

func (c *Config) CronPersistencePath() string {
	if c.Cron.PersistencePath == "" {
		return filepath.Join(c.DataDir(), "cron")
	}
	return c.Cron.PersistencePath
}
