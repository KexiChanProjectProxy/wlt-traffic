package runtime

import (
	"testing"

	"github.com/kexi/traffic-count/internal/bootstrap"
	"github.com/kexi/traffic-count/internal/config"
)

func TestNewService(t *testing.T) {
	cfg := config.New()
	cfg.Interfaces = []string{"lo"}
	cfg.DatabasePath = "/tmp/test.db"

	svc := NewService(cfg, "")

	if svc == nil {
		t.Fatal("NewService returned nil")
	}

	if svc.status == nil {
		t.Error("status is nil")
	}

	if svc.status.Mode != bootstrap.ModeHealthy {
		t.Errorf("initial mode = %v, want %v", svc.status.Mode, bootstrap.ModeHealthy)
	}

	if svc.status.TotalInterfaces != 1 {
		t.Errorf("TotalInterfaces = %d, want 1", svc.status.TotalInterfaces)
	}

	if len(svc.status.AttachedInterfaces) != 0 {
		t.Error("AttachedInterfaces should be empty initially")
	}

	if len(svc.status.FailedInterfaces) != 0 {
		t.Error("FailedInterfaces should be empty initially")
	}
}

func TestServiceIsHealthy(t *testing.T) {
	tests := []struct {
		name     string
		mode     bootstrap.Mode
		expected bool
	}{
		{"healthy mode", bootstrap.ModeHealthy, true},
		{"degraded mode", bootstrap.ModeDegraded, false},
		{"failed mode", bootstrap.ModeFailed, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{
				status: &Status{Mode: tt.mode},
			}
			if got := svc.IsHealthy(); got != tt.expected {
				t.Errorf("IsHealthy() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestServiceIsDegraded(t *testing.T) {
	tests := []struct {
		name     string
		mode     bootstrap.Mode
		expected bool
	}{
		{"healthy mode", bootstrap.ModeHealthy, false},
		{"degraded mode", bootstrap.ModeDegraded, true},
		{"failed mode", bootstrap.ModeFailed, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{
				status: &Status{Mode: tt.mode},
			}
			if got := svc.IsDegraded(); got != tt.expected {
				t.Errorf("IsDegraded() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestServiceIsFailed(t *testing.T) {
	tests := []struct {
		name     string
		mode     bootstrap.Mode
		expected bool
	}{
		{"healthy mode", bootstrap.ModeHealthy, false},
		{"degraded mode", bootstrap.ModeDegraded, false},
		{"failed mode", bootstrap.ModeFailed, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{
				status: &Status{Mode: tt.mode},
			}
			if got := svc.IsFailed(); got != tt.expected {
				t.Errorf("IsFailed() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestServiceGetStatus(t *testing.T) {
	status := &Status{
		Mode:               bootstrap.ModeDegraded,
		TotalInterfaces:    2,
		AttachedInterfaces: []string{"eth0"},
		FailedInterfaces:   []string{"eth1"},
		DatabasePath:       "/tmp/test.db",
	}

	svc := &Service{status: status}

	got := svc.GetStatus()
	if got != status {
		t.Error("GetStatus() should return the same status pointer")
	}

	if got.Mode != bootstrap.ModeDegraded {
		t.Errorf("status.Mode = %v, want %v", got.Mode, bootstrap.ModeDegraded)
	}
}

func TestServiceUpdateFromResult(t *testing.T) {
	svc := NewService(config.New(), "")
	svc.status.Mode = bootstrap.ModeHealthy
	svc.status.TotalInterfaces = 0
	svc.status.AttachedInterfaces = nil
	svc.status.FailedInterfaces = nil

	result := &bootstrap.StartupResult{
		Mode:               bootstrap.ModeDegraded,
		TotalInterfaces:    2,
		AttachedInterfaces: []string{"eth0", "wg0"},
		FailedInterfaces:   []string{"eth1"},
	}

	svc.UpdateFromResult(result)

	if svc.status.Mode != bootstrap.ModeDegraded {
		t.Errorf("Mode = %v, want %v", svc.status.Mode, bootstrap.ModeDegraded)
	}

	if svc.status.TotalInterfaces != 2 {
		t.Errorf("TotalInterfaces = %d, want 2", svc.status.TotalInterfaces)
	}

	if len(svc.status.AttachedInterfaces) != 2 {
		t.Errorf("AttachedInterfaces len = %d, want 2", len(svc.status.AttachedInterfaces))
	}

	if len(svc.status.FailedInterfaces) != 1 {
		t.Errorf("FailedInterfaces len = %d, want 1", len(svc.status.FailedInterfaces))
	}
}

func TestServiceGetAttachmentManager(t *testing.T) {
	svc := NewService(config.New(), "")
	manager := svc.GetAttachmentManager()
	if manager == nil {
		t.Error("GetAttachmentManager() returned nil")
	}
}

func TestServiceGetTrafficMap(t *testing.T) {
	svc := NewService(config.New(), "")
	trafficMap := svc.GetTrafficMap()
	if trafficMap == nil {
		t.Log("GetTrafficMap() returned nil (expected in mock/empty manager mode)")
	}
}
