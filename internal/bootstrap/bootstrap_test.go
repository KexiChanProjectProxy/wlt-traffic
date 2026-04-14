package bootstrap

import (
	"fmt"
	"testing"

	"github.com/kexi/traffic-count/internal/config"
)

func TestValidatorValidate_ZeroAttach_FailFast(t *testing.T) {
	cfg := &config.Config{
		Interfaces:    []string{"nonexistent0", "nonexistent1"},
		BindAddress:   "127.0.0.1:8080",
		DatabasePath:  "/tmp/test.db",
		FlushInterval: 10,
		LogLevel:      "info",
		AllowPartial:  false,
	}

	v := NewValidator(cfg)
	v.checkCapability = func() error { return nil }
	v.checkInterface = func(iface string) error {
		return fmt.Errorf("interface %q does not exist", iface)
	}
	v.checkQdisc = func(iface string) error { return nil }

	result, err := v.Validate()

	if err == nil {
		t.Fatal("expected error for zero attaches with allow_partial=false")
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Mode != ModeFailed {
		t.Errorf("Mode = %v, want %v", result.Mode, ModeFailed)
	}

	if len(result.AttachedInterfaces) != 0 {
		t.Errorf("AttachedInterfaces = %v, want empty", result.AttachedInterfaces)
	}

	if len(result.FailedInterfaces) != 2 {
		t.Errorf("FailedInterfaces = %v, want 2", result.FailedInterfaces)
	}
}

func TestValidatorValidate_PartialAttach_Degraded(t *testing.T) {
	cfg := &config.Config{
		Interfaces:    []string{"lo", "nonexistent0"},
		BindAddress:   "127.0.0.1:8080",
		DatabasePath:  "/tmp/test.db",
		FlushInterval: 10,
		LogLevel:      "info",
		AllowPartial:  true,
	}

	v := NewValidator(cfg)
	v.checkCapability = func() error { return nil }
	v.checkInterface = func(iface string) error {
		if iface == "nonexistent0" {
			return fmt.Errorf("interface %q does not exist", iface)
		}
		return nil
	}
	v.checkQdisc = func(iface string) error { return nil }

	result, err := v.Validate()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Mode != ModeDegraded {
		t.Errorf("Mode = %v, want %v", result.Mode, ModeDegraded)
	}

	if len(result.AttachedInterfaces) != 1 {
		t.Errorf("AttachedInterfaces = %v, want 1", result.AttachedInterfaces)
	}

	if len(result.FailedInterfaces) != 1 {
		t.Errorf("FailedInterfaces = %v, want 1", result.FailedInterfaces)
	}

	if result.FailedInterfaces[0] != "nonexistent0" {
		t.Errorf("FailedInterfaces[0] = %q, want %q", result.FailedInterfaces[0], "nonexistent0")
	}
}

func TestValidatorValidate_PartialAttach_FailFast(t *testing.T) {
	cfg := &config.Config{
		Interfaces:    []string{"lo", "nonexistent0"},
		BindAddress:   "127.0.0.1:8080",
		DatabasePath:  "/tmp/test.db",
		FlushInterval: 10,
		LogLevel:      "info",
		AllowPartial:  false,
	}

	v := NewValidator(cfg)
	v.checkCapability = func() error { return nil }
	v.checkInterface = func(iface string) error {
		if iface == "nonexistent0" {
			return fmt.Errorf("interface %q does not exist", iface)
		}
		return nil
	}
	v.checkQdisc = func(iface string) error { return nil }

	result, err := v.Validate()

	if err == nil {
		t.Fatal("expected error for partial attach with allow_partial=false")
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Mode != ModeFailed {
		t.Errorf("Mode = %v, want %v", result.Mode, ModeFailed)
	}
}

func TestValidatorValidate_AllAttach_Healthy(t *testing.T) {
	cfg := &config.Config{
		Interfaces:    []string{"lo"},
		BindAddress:   "127.0.0.1:8080",
		DatabasePath:  "/tmp/test.db",
		FlushInterval: 10,
		LogLevel:      "info",
		AllowPartial:  false,
	}

	v := NewValidator(cfg)
	v.checkCapability = func() error { return nil }
	v.checkInterface = func(iface string) error { return nil }
	v.checkQdisc = func(iface string) error { return nil }

	result, err := v.Validate()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Mode != ModeHealthy {
		t.Errorf("Mode = %v, want %v", result.Mode, ModeHealthy)
	}

	if len(result.AttachedInterfaces) != 1 {
		t.Errorf("AttachedInterfaces = %v, want 1", result.AttachedInterfaces)
	}

	if len(result.FailedInterfaces) != 0 {
		t.Errorf("FailedInterfaces = %v, want empty", result.FailedInterfaces)
	}
}

func TestValidatorValidate_CapabilityFailure(t *testing.T) {
	cfg := &config.Config{
		Interfaces:    []string{"lo"},
		BindAddress:   "127.0.0.1:8080",
		DatabasePath:  "/tmp/test.db",
		FlushInterval: 10,
		LogLevel:      "info",
		AllowPartial:  false,
	}

	v := NewValidator(cfg)
	v.checkCapability = func() error {
		return fmt.Errorf("root privileges required")
	}
	v.checkInterface = func(iface string) error { return nil }
	v.checkQdisc = func(iface string) error { return nil }

	result, err := v.Validate()

	if err == nil {
		t.Fatal("expected error for capability failure")
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Mode != ModeFailed {
		t.Errorf("Mode = %v, want %v", result.Mode, ModeFailed)
	}
}

func TestStartupResult(t *testing.T) {
	result := &StartupResult{
		Mode:               ModeHealthy,
		TotalInterfaces:    2,
		AttachedInterfaces: []string{"lo", "eth0"},
		FailedInterfaces:   []string{},
		AttachErrors:       []string{},
	}

	if result.Mode != ModeHealthy {
		t.Errorf("Mode = %v, want %v", result.Mode, ModeHealthy)
	}

	if result.TotalInterfaces != 2 {
		t.Errorf("TotalInterfaces = %d, want %d", result.TotalInterfaces, 2)
	}

	if len(result.AttachedInterfaces) != 2 {
		t.Errorf("AttachedInterfaces = %v, want 2 items", result.AttachedInterfaces)
	}
}
