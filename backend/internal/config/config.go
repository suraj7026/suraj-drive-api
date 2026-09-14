package config

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server struct {
		Port                  int    `mapstructure:"port"`
		FrontendURL           string `mapstructure:"frontend_url"`
		IsProd                bool   `mapstructure:"is_production"`
		ReadTimeoutSecs       int    `mapstructure:"read_timeout_secs"`
		WriteTimeoutSecs      int    `mapstructure:"write_timeout_secs"`
		ReadHeaderTimeoutSecs int    `mapstructure:"read_header_timeout_secs"`
		IdleTimeoutSecs       int    `mapstructure:"idle_timeout_secs"`
		TrustedProxyCIDRs     string `mapstructure:"trusted_proxy_cidrs"`
	} `mapstructure:"server"`
	Google struct {
		ClientID      string `mapstructure:"client_id"`
		ClientSecret  string `mapstructure:"client_secret"`
		RedirectURL   string `mapstructure:"redirect_url"`
		AllowedDomain string `mapstructure:"allowed_domain"`
	} `mapstructure:"google"`
	JWT struct {
		Secret    string `mapstructure:"secret"`
		ExpiryHrs int    `mapstructure:"expiry_hrs"`
	} `mapstructure:"jwt"`
	Database struct {
		URL                  string `mapstructure:"url"`
		MigrationURL         string `mapstructure:"migration_url"`
		MaxConnections       int    `mapstructure:"max_connections"`
		MinConnections       int    `mapstructure:"min_connections"`
		HealthTimeoutSecs    int    `mapstructure:"health_timeout_secs"`
		MaxConnectionAgeMins int    `mapstructure:"max_connection_age_mins"`
	} `mapstructure:"database"`
	MinIO struct {
		Endpoint       string `mapstructure:"endpoint"`
		PublicEndpoint string `mapstructure:"public_endpoint"`
		AccessKey      string `mapstructure:"access_key"`
		SecretKey      string `mapstructure:"secret_key"`
		BucketPrefix   string `mapstructure:"bucket_prefix"`
		UseSSL         bool   `mapstructure:"use_ssl"`
		Region         string `mapstructure:"region"`
	} `mapstructure:"minio"`
	Security struct {
		MalwareScanEnabled bool   `mapstructure:"malware_scan_enabled"`
		ClamAVAddress      string `mapstructure:"clamav_address"`
		ScanTimeoutSecs    int    `mapstructure:"scan_timeout_secs"`
		ScanWorkers        int    `mapstructure:"scan_workers"`
	} `mapstructure:"security"`
	Notifications struct {
		Enabled        bool   `mapstructure:"enabled"`
		SMTPAddress    string `mapstructure:"smtp_address"`
		SMTPUsername   string `mapstructure:"smtp_username"`
		SMTPPassword   string `mapstructure:"smtp_password"`
		FromAddress    string `mapstructure:"from_address"`
		FromName       string `mapstructure:"from_name"`
		PublicURL      string `mapstructure:"public_url"`
		TimeoutSecs    int    `mapstructure:"timeout_secs"`
		Workers        int    `mapstructure:"workers"`
		AllowPlaintext bool   `mapstructure:"allow_plaintext"`
	} `mapstructure:"notifications"`
}

func Load() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if err := bindEnvKeys(); err != nil {
		return nil, err
	}

	viper.SetDefault("server.port", 4001)
	viper.SetDefault("server.read_timeout_secs", 15)
	viper.SetDefault("server.write_timeout_secs", 15)
	viper.SetDefault("server.read_header_timeout_secs", 5)
	viper.SetDefault("server.idle_timeout_secs", 60)
	viper.SetDefault("jwt.expiry_hrs", 24)
	viper.SetDefault("database.max_connections", 20)
	viper.SetDefault("database.min_connections", 2)
	viper.SetDefault("database.health_timeout_secs", 5)
	viper.SetDefault("database.max_connection_age_mins", 30)
	viper.SetDefault("minio.bucket_prefix", "drive")
	viper.SetDefault("minio.region", "us-east-1")
	viper.SetDefault("security.malware_scan_enabled", false)
	viper.SetDefault("security.scan_timeout_secs", 300)
	viper.SetDefault("security.scan_workers", 2)
	viper.SetDefault("notifications.timeout_secs", 30)
	viper.SetDefault("notifications.workers", 2)

	if err := viper.ReadInConfig(); err != nil {
		var configNotFound viper.ConfigFileNotFoundError
		if !errors.As(err, &configNotFound) {
			return nil, err
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func bindEnvKeys() error {
	keys := []string{
		"server.port",
		"server.frontend_url",
		"server.is_production",
		"server.read_timeout_secs",
		"server.write_timeout_secs",
		"server.read_header_timeout_secs",
		"server.idle_timeout_secs",
		"server.trusted_proxy_cidrs",
		"google.client_id",
		"google.client_secret",
		"google.redirect_url",
		"google.allowed_domain",
		"jwt.secret",
		"jwt.expiry_hrs",
		"database.url",
		"database.migration_url",
		"database.max_connections",
		"database.min_connections",
		"database.health_timeout_secs",
		"database.max_connection_age_mins",
		"minio.endpoint",
		"minio.public_endpoint",
		"minio.access_key",
		"minio.secret_key",
		"minio.bucket_prefix",
		"minio.use_ssl",
		"minio.region",
		"security.malware_scan_enabled",
		"security.clamav_address",
		"security.scan_timeout_secs",
		"security.scan_workers",
		"notifications.enabled",
		"notifications.smtp_address",
		"notifications.smtp_username",
		"notifications.smtp_password",
		"notifications.from_address",
		"notifications.from_name",
		"notifications.public_url",
		"notifications.timeout_secs",
		"notifications.workers",
		"notifications.allow_plaintext",
	}

	for _, key := range keys {
		if err := viper.BindEnv(key); err != nil {
			return err
		}
	}

	return nil
}

func (c *Config) validate() error {
	if c.Server.Port <= 0 {
		return fmt.Errorf("server.port must be greater than 0")
	}
	if strings.TrimSpace(c.Server.FrontendURL) == "" {
		return fmt.Errorf("server.frontend_url is required")
	}
	if c.Server.ReadTimeoutSecs <= 0 || c.Server.WriteTimeoutSecs <= 0 || c.Server.ReadHeaderTimeoutSecs <= 0 || c.Server.IdleTimeoutSecs <= 0 {
		return fmt.Errorf("server timeouts must be greater than 0")
	}
	for _, rawCIDR := range strings.Split(c.Server.TrustedProxyCIDRs, ",") {
		rawCIDR = strings.TrimSpace(rawCIDR)
		if rawCIDR == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(rawCIDR); err != nil {
			return fmt.Errorf("server.trusted_proxy_cidrs contains invalid CIDR %q", rawCIDR)
		}
	}
	if strings.TrimSpace(c.Google.ClientID) == "" || strings.TrimSpace(c.Google.ClientSecret) == "" || strings.TrimSpace(c.Google.RedirectURL) == "" {
		return fmt.Errorf("google.client_id, google.client_secret, and google.redirect_url are required")
	}
	if strings.TrimSpace(c.JWT.Secret) == "" {
		return fmt.Errorf("jwt.secret is required")
	}
	if c.JWT.ExpiryHrs <= 0 {
		return fmt.Errorf("jwt.expiry_hrs must be greater than 0")
	}
	if strings.TrimSpace(c.Database.URL) == "" {
		return fmt.Errorf("database.url is required")
	}
	if c.Database.MaxConnections <= 0 {
		return fmt.Errorf("database.max_connections must be greater than 0")
	}
	if c.Database.MinConnections < 0 || c.Database.MinConnections > c.Database.MaxConnections {
		return fmt.Errorf("database.min_connections must be between 0 and database.max_connections")
	}
	if c.Database.HealthTimeoutSecs <= 0 || c.Database.MaxConnectionAgeMins <= 0 {
		return fmt.Errorf("database timeouts must be greater than 0")
	}
	if strings.TrimSpace(c.MinIO.Endpoint) == "" || strings.TrimSpace(c.MinIO.AccessKey) == "" || strings.TrimSpace(c.MinIO.SecretKey) == "" {
		return fmt.Errorf("minio.endpoint, minio.access_key, and minio.secret_key are required")
	}
	if strings.TrimSpace(c.MinIO.BucketPrefix) == "" {
		return fmt.Errorf("minio.bucket_prefix is required")
	}
	if c.Security.ScanTimeoutSecs <= 0 || c.Security.ScanWorkers <= 0 || c.Security.ScanWorkers > 16 {
		return fmt.Errorf("security scan timeout and worker count must be positive, with at most 16 workers")
	}
	if c.Security.MalwareScanEnabled && strings.TrimSpace(c.Security.ClamAVAddress) == "" {
		return fmt.Errorf("security.clamav_address is required when malware scanning is enabled")
	}
	if c.Notifications.TimeoutSecs <= 0 || c.Notifications.Workers <= 0 || c.Notifications.Workers > 16 {
		return fmt.Errorf("notification timeout and worker count must be positive, with at most 16 workers")
	}
	if c.Notifications.Enabled {
		if strings.TrimSpace(c.Notifications.SMTPAddress) == "" || strings.TrimSpace(c.Notifications.FromAddress) == "" || strings.TrimSpace(c.Notifications.PublicURL) == "" {
			return fmt.Errorf("notifications SMTP address, from address, and public URL are required when delivery is enabled")
		}
		if (c.Notifications.SMTPUsername == "") != (c.Notifications.SMTPPassword == "") {
			return fmt.Errorf("notifications SMTP username and password must be configured together")
		}
	}
	if c.Server.IsProd {
		if !c.Security.MalwareScanEnabled {
			return fmt.Errorf("security.malware_scan_enabled must be true in production")
		}
		if !c.Notifications.Enabled {
			return fmt.Errorf("notifications.enabled must be true in production")
		}
		if c.Notifications.AllowPlaintext {
			return fmt.Errorf("plaintext SMTP is forbidden in production")
		}
		if !strings.HasPrefix(strings.ToLower(c.Notifications.PublicURL), "https://") {
			return fmt.Errorf("notifications.public_url must use HTTPS in production")
		}
		if len(c.JWT.Secret) < 32 || strings.Contains(strings.ToLower(c.JWT.Secret), "replace-with") {
			return fmt.Errorf("jwt.secret must be a non-placeholder value of at least 32 characters in production")
		}
		if strings.Contains(strings.ToLower(c.Google.ClientID), "replace-with") || strings.Contains(strings.ToLower(c.Google.ClientSecret), "replace-with") {
			return fmt.Errorf("Google OAuth placeholders are forbidden in production")
		}
		if strings.Contains(strings.ToLower(c.MinIO.AccessKey), "replace-with") || strings.Contains(strings.ToLower(c.MinIO.SecretKey), "replace-with") {
			return fmt.Errorf("MinIO placeholders are forbidden in production")
		}
		if !c.MinIO.UseSSL {
			return fmt.Errorf("minio.use_ssl must be true in production")
		}
		if !strings.HasPrefix(strings.ToLower(c.Server.FrontendURL), "https://") || !strings.HasPrefix(strings.ToLower(c.Google.RedirectURL), "https://") {
			return fmt.Errorf("frontend and OAuth callback URLs must use HTTPS in production")
		}
		if strings.Contains(strings.ToLower(c.Database.URL), "sslmode=disable") {
			return fmt.Errorf("database TLS cannot be disabled in production")
		}
	}
	return nil
}
