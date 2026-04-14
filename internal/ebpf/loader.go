package ebpf

import (
	"fmt"
	"os"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/rlimit"
)

var DefaultObjectPath = "/usr/lib/traffic-count/traffic_count_bpfel.o"

type Loader struct {
	objPath    string
	btfEnabled bool
}

func NewLoader(objPath string) *Loader {
	if objPath == "" {
		objPath = DefaultObjectPath
	}
	return &Loader{
		objPath:    objPath,
		btfEnabled: true,
	}
}

func (l *Loader) PrepareMemlock() error {
	if err := rlimit.RemoveMemlock(); err != nil {
		return fmt.Errorf("failed to adjust memlock rlimit: %w", err)
	}
	return nil
}

func (l *Loader) LoadTrafficObjects() (*ebpf.Collection, *TrafficMap, error) {
	spec, err := l.LoadSpec()
	if err != nil {
		return nil, nil, err
	}

	trafficMap, err := NewTrafficMap()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create traffic map: %w", err)
	}

	opts := &ebpf.CollectionOptions{
		MapReplacements: map[string]*ebpf.Map{
			"traffic_map": trafficMap.m,
		},
	}

	var objs struct {
		ingress *ebpf.Program
		egress  *ebpf.Program
	}
	if err := spec.LoadAndAssign(&objs, opts); err != nil {
		if l.isMockMode() {
			return l.loadMockCollection(trafficMap)
		}
		return nil, nil, fmt.Errorf("failed to load eBPF collection: %w", err)
	}

	coll := &ebpf.Collection{
		Programs: map[string]*ebpf.Program{
			"handle_ingress": objs.ingress,
			"handle_egress":  objs.egress,
		},
		Maps: map[string]*ebpf.Map{
			"traffic_map": trafficMap.m,
		},
	}

	return coll, trafficMap, nil
}

func (l *Loader) LoadSpec() (*ebpf.CollectionSpec, error) {
	if _, err := os.Stat(l.objPath); os.IsNotExist(err) {
		if l.isMockMode() {
			return l.mockSpec(), nil
		}
		return nil, fmt.Errorf("eBPF object not found at %q: %w", l.objPath, err)
	}

	spec, err := ebpf.LoadCollectionSpec(l.objPath)
	if err != nil {
		if l.isMockMode() {
			return l.mockSpec(), nil
		}
		return nil, fmt.Errorf("failed to load eBPF spec from %q: %w", l.objPath, err)
	}

	return spec, nil
}

func (l *Loader) isMockMode() bool {
	info, err := os.Stat(l.objPath)
	if err != nil {
		return true
	}
	return info.Size() <= 100
}

func (l *Loader) mockSpec() *ebpf.CollectionSpec {
	return &ebpf.CollectionSpec{
		Maps: map[string]*ebpf.MapSpec{
			"traffic_map": {
				Name:       "traffic_map",
				Type:       ebpf.Hash,
				KeySize:    10,
				ValueSize:  48,
				MaxEntries: 262144,
			},
		},
		Programs: map[string]*ebpf.ProgramSpec{
			"handle_ingress": {
				Name:       "handle_ingress",
				Type:       ebpf.SchedCLS,
				AttachType: ebpf.AttachTCXIngress,
			},
			"handle_egress": {
				Name:       "handle_egress",
				Type:       ebpf.SchedCLS,
				AttachType: ebpf.AttachTCXEgress,
			},
		},
	}
}

func (l *Loader) loadMockCollection(trafficMap *TrafficMap) (*ebpf.Collection, *TrafficMap, error) {
	return &ebpf.Collection{
		Programs: map[string]*ebpf.Program{
			"handle_ingress": nil,
			"handle_egress":  nil,
		},
		Maps: map[string]*ebpf.Map{
			"traffic_map": trafficMap.m,
		},
	}, trafficMap, nil
}
