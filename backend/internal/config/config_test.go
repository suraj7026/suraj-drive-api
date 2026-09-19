package config

import (
	"strings"
	"testing"
)

func validTestConfig() *Config {
	cfg := &Config{}
	cfg.Server.Port = 4001
	cfg.Server.FrontendURL = "http://localhost:4000"
	cfg.Server.ReadTimeoutSecs = 15
	cfg.Server.WriteTimeoutSecs = 15
	cfg.Server.ReadHeaderTimeoutSecs = 5
	cfg.Server.IdleTimeoutSecs = 60
	cfg.Google.ClientID = "test-client"
	cfg.Google.ClientSecret = "test-secret"
	cfg.Google.RedirectURL = "http://localhost:4001/api/auth/google/callback"
	cfg.JWT.Secret = "test-jwt-secret"
	cfg.JWT.ExpiryHrs = 24
	cfg.Database.URL = "postgres://app:password@localhost/drive?sslmode=require"
	cfg.Database.MaxConnections = 20
	cfg.Database.MinConnections = 2
	cfg.Database.HealthTimeoutSecs = 5
	cfg.Database.MaxConnectionAgeMins = 30
	cfg.MinIO.Endpoint = "localhost:9000"
	cfg.MinIO.AccessKey = "test-access"
	cfg.MinIO.SecretKey = "test-secret"
	cfg.MinIO.BucketPrefix = "drive"
	cfg.Security.ScanTimeoutSecs = 300
	cfg.Security.ScanWorkers = 2
	cfg.Notifications.TimeoutSecs = 30
	cfg.Notifications.Workers = 2
	return cfg
}

func TestValidateAllowsDevelopmentConfiguration(t *testing.T) {
	if err := validTestConfig().validate(); err != nil {
		t.Fatalf("valid development configuration failed: %v", err)
	}
}

func TestValidateRejectsInvalidTrustedProxyCIDR(t *testing.T) {
	cfg := validTestConfig()
	cfg.Server.TrustedProxyCIDRs = "172.16.0.0/12, definitely-not-a-network"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "trusted_proxy_cidrs") {
		t.Fatalf("expected trusted proxy validation error, got %v", err)
	}
}

func TestValidateRejectsInsecureProductionConfiguration(t *testing.T) {
	cfg := validTestConfig()
	cfg.Server.IsProd = true
	cfg.Security.MalwareScanEnabled = true
	cfg.Security.ClamAVAddress = "clamav:3310"
	cfg.Notifications.Enabled = true
	cfg.Notifications.SMTPAddress = "smtp.example.com:587"
	cfg.Notifications.FromAddress = "drive@example.com"
	cfg.Notifications.PublicURL = "https://drive.example.com"
	cfg.JWT.Secret = "replace-with-a-strong-random-secret"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "jwt.secret") {
		t.Fatalf("expected production JWT validation error, got %v", err)
	}
}

func TestValidateRequiresMalwareScanningInProduction(t *testing.T) {
	cfg := validTestConfig()
	cfg.Server.IsProd = true
	cfg.JWT.Secret = "0123456789abcdef0123456789abcdef"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "malware_scan_enabled") {
		t.Fatalf("expected production malware scanning validation error, got %v", err)
	}
}

func TestValidateRequiresNotificationDeliveryInProduction(t *testing.T) {
	cfg := validTestConfig()
	cfg.Server.IsProd = true
	cfg.Server.FrontendURL = "https://drive.example.com"
	cfg.Google.RedirectURL = "https://api.drive.example.com/api/auth/google/callback"
	cfg.JWT.Secret = "0123456789abcdef0123456789abcdef"
	cfg.MinIO.UseSSL = true
	cfg.Security.MalwareScanEnabled = true
	cfg.Security.ClamAVAddress = "clamav:3310"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "notifications.enabled") {
		t.Fatalf("expected production notification delivery validation error, got %v", err)
	}
}

func TestValidateAllowsSecureProductionConfiguration(t *testing.T) {
	cfg := validTestConfig()
	cfg.Server.IsProd = true
	cfg.Server.FrontendURL = "https://drive.example.com"
	cfg.Google.RedirectURL = "https://api.drive.example.com/api/auth/google/callback"
	cfg.JWT.Secret = "0123456789abcdef0123456789abcdef"
	cfg.MinIO.UseSSL = true
	cfg.Security.MalwareScanEnabled = true
	cfg.Security.ClamAVAddress = "clamav:3310"
	cfg.Notifications.Enabled = true
	cfg.Notifications.SMTPAddress = "smtp.example.com:587"
	cfg.Notifications.FromAddress = "drive@example.com"
	cfg.Notifications.PublicURL = "https://drive.example.com"
	if err := cfg.validate(); err != nil {
		t.Fatalf("valid production configuration failed: %v", err)
	}
}
