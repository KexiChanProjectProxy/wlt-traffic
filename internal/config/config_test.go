package config

import (
	"testing"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config with all fields",
			cfg: &Config{
				Interfaces:    []string{"eth0", "lo"},
				BindAddress:   "127.0.0.1:8080",
				DatabasePath:  "/tmp/test.db",
				FlushInterval: 10,
				LogLevel:      "info",
				AllowPartial:  false,
			},
			wantErr: false,
		},
		{
			name: "empty interfaces",
			cfg: &Config{
				Interfaces:    []string{},
				BindAddress:   "127.0.0.1:8080",
				DatabasePath:  "/tmp/test.db",
				FlushInterval: 10,
				LogLevel:      "info",
			},
			wantErr: true,
			errMsg:  "no interfaces configured",
		},
		{
			name: "empty interface name in list",
			cfg: &Config{
				Interfaces:    []string{"eth0", "", "lo"},
				BindAddress:   "127.0.0.1:8080",
				DatabasePath:  "/tmp/test.db",
				FlushInterval: 10,
				LogLevel:      "info",
			},
			wantErr: true,
			errMsg:  "empty interface name",
		},
		{
			name: "missing bind address",
			cfg: &Config{
				Interfaces:    []string{"eth0"},
				BindAddress:   "",
				DatabasePath:  "/tmp/test.db",
				FlushInterval: 10,
				LogLevel:      "info",
			},
			wantErr: true,
			errMsg:  "bind address is required",
		},
		{
			name: "non-loopback bind address",
			cfg: &Config{
				Interfaces:    []string{"eth0"},
				BindAddress:   "0.0.0.0:8080",
				DatabasePath:  "/tmp/test.db",
				FlushInterval: 10,
				LogLevel:      "info",
			},
			wantErr: true,
			errMsg:  "must be loopback",
		},
		{
			name: "invalid port",
			cfg: &Config{
				Interfaces:    []string{"eth0"},
				BindAddress:   "127.0.0.1:0",
				DatabasePath:  "/tmp/test.db",
				FlushInterval: 10,
				LogLevel:      "info",
			},
			wantErr: true,
			errMsg:  "port must be between 1 and 65535",
		},
		{
			name: "invalid log level",
			cfg: &Config{
				Interfaces:    []string{"eth0"},
				BindAddress:   "127.0.0.1:8080",
				DatabasePath:  "/tmp/test.db",
				FlushInterval: 10,
				LogLevel:      "invalid",
			},
			wantErr: true,
			errMsg:  "invalid log level",
		},
		{
			name: "zero flush interval",
			cfg: &Config{
				Interfaces:    []string{"eth0"},
				BindAddress:   "127.0.0.1:8080",
				DatabasePath:  "/tmp/test.db",
				FlushInterval: 0,
				LogLevel:      "info",
			},
			wantErr: true,
			errMsg:  "flush interval must be positive",
		},
		{
			name: "negative flush interval",
			cfg: &Config{
				Interfaces:    []string{"eth0"},
				BindAddress:   "127.0.0.1:8080",
				DatabasePath:  "/tmp/test.db",
				FlushInterval: -1,
				LogLevel:      "info",
			},
			wantErr: true,
			errMsg:  "flush interval must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" {
				if err == nil || !contains(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, expected to contain %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestNewConfig(t *testing.T) {
	cfg := New()
	if cfg.BindAddress != "127.0.0.1:8080" {
		t.Errorf("New() BindAddress = %q, want %q", cfg.BindAddress, "127.0.0.1:8080")
	}
	if cfg.DatabasePath != "./traffic-count.db" {
		t.Errorf("New() DatabasePath = %q, want %q", cfg.DatabasePath, "./traffic-count.db")
	}
	if cfg.FlushInterval != 10 {
		t.Errorf("New() FlushInterval = %d, want %d", cfg.FlushInterval, 10)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("New() LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.AllowPartial != false {
		t.Errorf("New() AllowPartial = %v, want %v", cfg.AllowPartial, false)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
