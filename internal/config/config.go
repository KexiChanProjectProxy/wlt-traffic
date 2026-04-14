// Package config provides configuration loading and validation.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

var ValidLogLevels = []string{"debug", "info", "warn", "error"}

type Config struct {
	Interfaces           []string
	BindAddress          string
	DatabasePath         string
	FlushInterval        int
	HousekeepingInterval int
	LogLevel             string
	AllowPartial         bool
}

func (c *Config) Validate() error {
	if len(c.Interfaces) == 0 {
		return fmt.Errorf("config validation failed: no interfaces configured")
	}
	for _, iface := range c.Interfaces {
		if strings.TrimSpace(iface) == "" {
			return fmt.Errorf("config validation failed: empty interface name in allowlist")
		}
	}
	if c.BindAddress == "" {
		return fmt.Errorf("config validation failed: bind address is required")
	}
	host, portStr, err := net.SplitHostPort(c.BindAddress)
	if err != nil {
		return fmt.Errorf("config validation failed: invalid bind address %q: %w", c.BindAddress, err)
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fmt.Errorf("config validation failed: bind address must be loopback (127.0.0.1 or localhost or ::1), got %q", host)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("config validation failed: invalid port in bind address %q: %w", c.BindAddress, err)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("config validation failed: port must be between 1 and 65535, got %d", port)
	}
	if c.DatabasePath == "" {
		return fmt.Errorf("config validation failed: database path is required")
	}
	if c.FlushInterval <= 0 {
		return fmt.Errorf("config validation failed: flush interval must be positive, got %d", c.FlushInterval)
	}
	validLogLevel := false
	for _, lvl := range ValidLogLevels {
		if c.LogLevel == lvl {
			validLogLevel = true
			break
		}
	}
	if !validLogLevel {
		return fmt.Errorf("config validation failed: invalid log level %q, must be one of: debug, info, warn, error", c.LogLevel)
	}
	return nil
}

func Load(path string) (*Config, error) {
	if path == "" {
		return New(), nil
	}
	return LoadFromFile(path)
}

func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	return Parse(data)
}

func Parse(data []byte) (*Config, error) {
	cfg := New()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}
	return cfg, nil
}

func New() *Config {
	return &Config{
		Interfaces:           []string{},
		BindAddress:          "127.0.0.1:8080",
		DatabasePath:         "./traffic-count.db",
		FlushInterval:        10,
		HousekeepingInterval: 3600,
		LogLevel:             "info",
		AllowPartial:         false,
	}
}
