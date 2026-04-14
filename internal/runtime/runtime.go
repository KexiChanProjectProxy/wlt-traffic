package runtime

import (
	"fmt"
	"strings"

	"github.com/kexi/traffic-count/internal/bootstrap"
	"github.com/kexi/traffic-count/internal/config"
	"github.com/kexi/traffic-count/internal/ebpf"
)

type Service struct {
	cfg       *config.Config
	status    *Status
	loader    *ebpf.Loader
	manager   *ebpf.AttachmentManager
	attachErr []string
}

type Status struct {
	Mode               bootstrap.Mode
	TotalInterfaces    int
	AttachedInterfaces []string
	FailedInterfaces   []string
	LastFlushTimestamp int64
	DatabasePath       string
	AttachErrors       []string
}

func NewService(cfg *config.Config) *Service {
	return &Service{
		cfg: cfg,
		status: &Status{
			Mode:               bootstrap.ModeHealthy,
			TotalInterfaces:    len(cfg.Interfaces),
			AttachedInterfaces: []string{},
			FailedInterfaces:   []string{},
			AttachErrors:       []string{},
			LastFlushTimestamp: 0,
			DatabasePath:       cfg.DatabasePath,
		},
		loader:  ebpf.NewLoader(""),
		manager: ebpf.NewAttachmentManager(),
	}
}

func (s *Service) Start() error {
	fmt.Printf("Starting traffic count service with interfaces: %v\n", s.cfg.Interfaces)

	if err := s.loader.PrepareMemlock(); err != nil {
		return fmt.Errorf("failed to prepare memlock: %w", err)
	}

	coll, trafficMap, err := s.loader.LoadTrafficObjects()
	if err != nil {
		return fmt.Errorf("failed to load eBPF objects: %w", err)
	}

	s.manager.SetCollection(coll, trafficMap)

	var attachErrors []string
	var attached []string
	var failed []string

	for _, ifaceName := range s.cfg.Interfaces {
		opts := ebpf.AttachOptions{
			Ingress: s.manager.GetIngress(ifaceName),
			Egress:  s.manager.GetEgress(ifaceName),
		}

		if opts.Ingress == nil && opts.Egress == nil {
			if s.manager.IsMockMode() {
				continue
			}
			attachErrors = append(attachErrors, fmt.Sprintf("no programs available for interface %q", ifaceName))
			failed = append(failed, ifaceName)
			continue
		}

		results, err := s.manager.AttachIface(ifaceName, opts)
		if err != nil {
			attachErrors = append(attachErrors, err.Error())
			failed = append(failed, ifaceName)
			continue
		}

		for _, res := range results {
			if res.State == ebpf.StateFailed {
				if res.Error != nil {
					attachErrors = append(attachErrors, fmt.Sprintf("interface %q %s: %v", ifaceName, res.Direction, res.Error))
				}
				if !contains(failed, ifaceName) {
					failed = append(failed, ifaceName)
				}
			}
		}

		ifaceState := s.manager.GetIfaceState(ifaceName)
		if ifaceState != nil {
			ingressOK := ifaceState.Ingress != nil && ifaceState.Ingress.State == ebpf.StateAttached
			egressOK := ifaceState.Egress != nil && ifaceState.Egress.State == ebpf.StateAttached
			if ingressOK || egressOK {
				attached = append(attached, ifaceName)
			}
		}
	}

	s.status.AttachedInterfaces = attached
	s.status.FailedInterfaces = failed
	s.status.AttachErrors = attachErrors

	if len(attached) == 0 && len(failed) > 0 {
		s.status.Mode = bootstrap.ModeFailed
		return fmt.Errorf("all interface attachments failed")
	}

	if len(failed) > 0 {
		if s.cfg.AllowPartial {
			s.status.Mode = bootstrap.ModeDegraded
		} else {
			s.status.Mode = bootstrap.ModeFailed
			return fmt.Errorf("partial interface attachment failure (allow_partial=false)")
		}
	}

	return nil
}

func (s *Service) Stop() error {
	fmt.Println("Stopping traffic count service...")
	results := s.manager.DetachAll()

	var detachErrors []string
	for _, res := range results {
		if res.State == ebpf.StateFailed {
			detachErrors = append(detachErrors, fmt.Sprintf("interface %q %s detach: %v", res.IfaceName, res.Direction, res.Error))
		}
	}

	for _, ifaceName := range s.cfg.Interfaces {
		ifaceState := s.manager.GetIfaceState(ifaceName)
		if ifaceState != nil {
			if ifaceState.Ingress != nil {
				ifaceState.Ingress.State = ebpf.StateDetached
				ifaceState.Ingress.Link = nil
			}
			if ifaceState.Egress != nil {
				ifaceState.Egress.State = ebpf.StateDetached
				ifaceState.Egress.Link = nil
			}
		}
	}

	s.status.AttachedInterfaces = []string{}
	s.status.Mode = bootstrap.ModeFailed

	if len(detachErrors) > 0 {
		fmt.Printf("detach errors: %v\n", detachErrors)
	}

	fmt.Println("Service shutdown complete")
	return nil
}

func (s *Service) GetStatus() *Status {
	return s.status
}

func (s *Service) UpdateFromResult(result *bootstrap.StartupResult) {
	s.status.Mode = result.Mode
	s.status.TotalInterfaces = result.TotalInterfaces
	s.status.AttachedInterfaces = result.AttachedInterfaces
	s.status.FailedInterfaces = result.FailedInterfaces
}

func (s *Service) IsHealthy() bool {
	return s.status.Mode == bootstrap.ModeHealthy
}

func (s *Service) IsDegraded() bool {
	return s.status.Mode == bootstrap.ModeDegraded
}

func (s *Service) IsFailed() bool {
	return s.status.Mode == bootstrap.ModeFailed
}

func (s *Service) GetAttachmentManager() *ebpf.AttachmentManager {
	return s.manager
}

func (s *Service) GetTrafficMap() *ebpf.TrafficMap {
	return s.manager.GetTrafficMap()
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func hasFailedInterface(ifaceName string, failed []string) bool {
	return contains(failed, ifaceName)
}

func stringsContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return strings.Contains(strings.Join(haystack, ","), needle)
}
