// Package config loads and validates the service configuration.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Interfaces are the LAN-facing L2 interfaces to account on (normally
	// just the LAN bridge, e.g. "br-lan"). Do not list a bridge together with
	// its member ports: the same frames would be counted twice.
	Interfaces []string `json:"interfaces"`
	// BindAddress is the loopback address the HTTP API listens on.
	BindAddress  string `json:"bind_address"`
	DatabasePath string `json:"database_path"`
	// FlushInterval is how often counters are sampled and written (seconds).
	FlushInterval int `json:"flush_interval"`
	// HousekeepingInterval is how often old daily rows are pruned (seconds).
	HousekeepingInterval int `json:"housekeeping_interval"`
	// RetentionDays is how long daily rows are kept. Totals are kept forever.
	RetentionDays int `json:"retention_days"`
	// Timezone decides where a day starts ("today", "month", ...). IANA name
	// such as "Asia/Shanghai"; empty or "Local" uses the system time zone.
	Timezone string `json:"timezone"`
	// AllowPartial lets the service start when some interfaces are missing;
	// they are attached as soon as they appear.
	AllowPartial bool `json:"allow_partial"`
	// LogLevel is accepted for compatibility and currently unused.
	LogLevel string `json:"log_level"`

	location *time.Location
}

func Default() *Config {
	return &Config{
		BindAddress:          "127.0.0.1:8081",
		DatabasePath:         "/var/lib/traffic-count/traffic-count.db",
		FlushInterval:        10,
		HousekeepingInterval: 3600,
		RetentionDays:        400,
		Timezone:             "Local",
		LogLevel:             "info",
	}
}

// Load reads a JSON config file on top of the defaults. Unknown keys are an
// error, so a misspelt key cannot silently fall back to a default.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		return nil, fmt.Errorf("no config file given (use --config)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := Parse(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Parse(data []byte, cfg *Config) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	return cfg.Validate()
}

func (c *Config) Validate() error {
	if len(c.Interfaces) == 0 {
		return fmt.Errorf("config: interfaces must not be empty")
	}
	seen := map[string]bool{}
	for _, i := range c.Interfaces {
		if strings.TrimSpace(i) == "" {
			return fmt.Errorf("config: empty interface name")
		}
		if seen[i] {
			return fmt.Errorf("config: interface %q listed twice", i)
		}
		seen[i] = true
	}
	host, port, err := net.SplitHostPort(c.BindAddress)
	if err != nil {
		return fmt.Errorf("config: invalid bind_address %q: %w", c.BindAddress, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("config: bind_address must be a loopback address, got %q", host)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("config: invalid port in bind_address %q", c.BindAddress)
	}
	if c.DatabasePath == "" {
		return fmt.Errorf("config: database_path is required")
	}
	if c.FlushInterval <= 0 {
		return fmt.Errorf("config: flush_interval must be positive")
	}
	if c.HousekeepingInterval <= 0 {
		return fmt.Errorf("config: housekeeping_interval must be positive")
	}
	if c.RetentionDays <= 0 {
		return fmt.Errorf("config: retention_days must be positive")
	}
	loc := time.Local
	if c.Timezone != "" && c.Timezone != "Local" {
		loc, err = time.LoadLocation(c.Timezone)
		if err != nil {
			return fmt.Errorf("config: invalid timezone %q: %w", c.Timezone, err)
		}
	}
	c.location = loc
	return nil
}

// Location is the time zone days are counted in (valid after Validate).
func (c *Config) Location() *time.Location {
	if c.location == nil {
		return time.Local
	}
	return c.location
}
