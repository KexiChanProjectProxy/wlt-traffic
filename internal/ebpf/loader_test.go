package ebpf

import (
	"strings"
	"testing"

	"github.com/cilium/ebpf"
)

func TestLoaderNewLoader(t *testing.T) {
	loader := NewLoader("")
	if loader == nil {
		t.Fatal("NewLoader returned nil")
	}

	loader2 := NewLoader("/custom/path.o")
	if loader2 == nil {
		t.Fatal("NewLoader with custom path returned nil")
	}
}

func TestLoaderPrepareMemlock(t *testing.T) {
	loader := NewLoader("")
	if err := loader.PrepareMemlock(); err != nil {
		t.Logf("PrepareMemlock warning (expected in some envs): %v", err)
	}
}

func TestLoaderIsMockMode(t *testing.T) {
	loader := NewLoader("/nonexistent/path.o")
	if !loader.isMockMode() {
		t.Error("loader with nonexistent path should be in mock mode")
	}
}

func TestLoaderMockSpec(t *testing.T) {
	loader := NewLoader("")
	spec := loader.mockSpec()
	if spec == nil {
		t.Fatal("mockSpec returned nil")
	}

	if len(spec.Maps) != 1 {
		t.Errorf("expected 1 map, got %d", len(spec.Maps))
	}

	if len(spec.Programs) != 2 {
		t.Errorf("expected 2 programs, got %d", len(spec.Programs))
	}

	mapSpec, ok := spec.Maps["traffic_map"]
	if !ok {
		t.Fatal("traffic_map not found in mock spec")
	}

	if mapSpec.MaxEntries != 262144 {
		t.Errorf("expected max_entries 262144, got %d", mapSpec.MaxEntries)
	}

	_, ok = spec.Programs["handle_ingress"]
	if !ok {
		t.Fatal("handle_ingress not found in mock spec")
	}

	_ = ebpf.AttachTCXIngress
}

func TestLoaderLoadSpecMockMode(t *testing.T) {
	loader := NewLoader("/nonexistent/object.o")
	spec, err := loader.LoadSpec()
	if err != nil {
		t.Fatalf("LoadSpec failed: %v", err)
	}

	if spec == nil {
		t.Fatal("LoadSpec returned nil in mock mode")
	}
}

func TestLoaderLoadTrafficObjectsMockMode(t *testing.T) {
	loader := NewLoader("/nonexistent/object.o")
	coll, trafficMap, err := loader.LoadTrafficObjects()
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") || strings.Contains(err.Error(), "MEMLOCK") {
			t.Skip("MEMLOCK too low, skipping map creation test")
		}
		t.Fatalf("LoadTrafficObjects failed: %v", err)
	}

	if coll == nil {
		t.Fatal("collection is nil")
	}

	if trafficMap == nil {
		t.Fatal("trafficMap is nil")
	}

	if trafficMap.m == nil {
		t.Fatal("trafficMap.m is nil")
	}
}
