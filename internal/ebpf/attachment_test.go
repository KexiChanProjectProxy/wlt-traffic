package ebpf

import (
	"testing"
)

func TestNewAttachmentManager(t *testing.T) {
	m := NewAttachmentManager()
	if m == nil {
		t.Fatal("NewAttachmentManager returned nil")
	}

	if m.ifaceStates == nil {
		t.Error("ifaceStates map is nil")
	}
}

func TestAttachmentManagerIsMockMode(t *testing.T) {
	m := NewAttachmentManager()
	if !m.IsMockMode() {
		t.Error("new manager should be in mock mode")
	}
}

func TestAttachmentManagerGetFailedInterfaces(t *testing.T) {
	m := NewAttachmentManager()
	failed := m.GetFailedInterfaces()
	if len(failed) != 0 {
		t.Errorf("expected empty failed list, got %v", failed)
	}
}

func TestAttachmentManagerGetAttachedInterfaces(t *testing.T) {
	m := NewAttachmentManager()
	attached := m.GetAttachedInterfaces()
	if len(attached) != 0 {
		t.Errorf("expected empty attached list, got %v", attached)
	}
}

func TestAttachmentManagerGetIfaceState(t *testing.T) {
	m := NewAttachmentManager()
	state := m.GetIfaceState("nonexistent")
	if state != nil {
		t.Error("GetIfaceState should return nil for unknown interface")
	}
}

func TestDirectionString(t *testing.T) {
	tests := []struct {
		dir      Direction
		expected string
	}{
		{DirectionIngress, "ingress"},
		{DirectionEgress, "egress"},
		{Direction(100), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.dir.String(); got != tt.expected {
			t.Errorf("Direction(%d).String() = %q, want %q", tt.dir, got, tt.expected)
		}
	}
}

func TestAttachStateConstants(t *testing.T) {
	if StateDetached != "detached" {
		t.Errorf("StateDetached = %q, want %q", StateDetached, "detached")
	}
	if StateAttached != "attached" {
		t.Errorf("StateAttached = %q, want %q", StateAttached, "attached")
	}
	if StateFailed != "failed" {
		t.Errorf("StateFailed = %q, want %q", StateFailed, "failed")
	}
}

func TestAttachmentManagerGetAllIfaceStates(t *testing.T) {
	m := NewAttachmentManager()
	states := m.GetAllIfaceStates()
	if states == nil {
		t.Error("GetAllIfaceStates returned nil")
	}
	if len(states) != 0 {
		t.Errorf("expected 0 states, got %d", len(states))
	}
}

func TestAttachmentManagerIsIfaceAttached(t *testing.T) {
	m := NewAttachmentManager()
	if m.IsIfaceAttached("nonexistent") {
		t.Error("untracked interface should not be attached")
	}
}

func TestAttachmentManagerSetCollection(t *testing.T) {
	m := NewAttachmentManager()
	m.SetCollection(nil, nil)

	if !m.IsMockMode() {
		t.Error("manager with nil collection should be in mock mode")
	}
}

func TestAttachmentManagerGetTrafficMap(t *testing.T) {
	m := NewAttachmentManager()
	tm := m.GetTrafficMap()
	if tm != nil {
		t.Error("new manager should have nil traffic map")
	}
}

func TestAttachmentManagerGetIngressEgress(t *testing.T) {
	m := NewAttachmentManager()
	ingress := m.GetIngress("lo")
	egress := m.GetEgress("lo")

	if ingress != nil {
		t.Error("nil collection should return nil ingress")
	}
	if egress != nil {
		t.Error("nil collection should return nil egress")
	}
}
