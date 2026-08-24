package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server struct {
		Port             int    `mapstructure:"port"`
		FrontendURL      string `mapstructure:"frontend_url"`
		IsProd           bool   `mapstructure:"is_production"`
		ReadTimeoutSecs  int    `mapstructure:"read_timeout_secs"`
		WriteTimeoutSecs int    `mapstructure:"write_timeout_secs"`
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
	viper.SetDefault("jwt.expiry_hrs", 24)
	viper.SetDefault("database.max_connections", 20)
	viper.SetDefault("database.min_connections", 2)
	viper.SetDefault("database.health_timeout_secs", 5)
	viper.SetDefault("database.max_connection_age_mins", 30)
	viper.SetDefault("minio.bucket_prefix", "drive")
	viper.SetDefault("minio.region", "us-east-1")

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
	if c.Server.ReadTimeoutSecs <= 0 || c.Server.WriteTimeoutSecs <= 0 {
		return fmt.Errorf("server timeouts must be greater than 0")
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
	return nil
}
