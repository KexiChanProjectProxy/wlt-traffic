package ebpf

import (
	"fmt"
	"net"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
)

type Direction int

const (
	DirectionIngress Direction = iota
	DirectionEgress
)

func (d Direction) String() string {
	switch d {
	case DirectionIngress:
		return "ingress"
	case DirectionEgress:
		return "egress"
	default:
		return "unknown"
	}
}

type AttachResult struct {
	IfaceName string
	Ifindex   int
	Direction Direction
	State     AttachState
	Error     error
}

type AttachState string

const (
	StateDetached AttachState = "detached"
	StateAttached AttachState = "attached"
	StateFailed   AttachState = "failed"
)

type AttachOptions struct {
	Ingress *ebpf.Program
	Egress  *ebpf.Program
}

type AttachmentManager struct {
	ifaceStates map[string]*IfaceState
	collection  *ebpf.Collection
	trafficMap  *TrafficMap
}

type IfaceState struct {
	Name    string
	Ifindex int
	Ingress *DirectionState
	Egress  *DirectionState
}

type DirectionState struct {
	State    AttachState
	Link     link.Link
	ErrorMsg string
}

func NewAttachmentManager() *AttachmentManager {
	return &AttachmentManager{
		ifaceStates: make(map[string]*IfaceState),
	}
}

func (m *AttachmentManager) AttachIface(ifaceName string, opts AttachOptions) ([]AttachResult, error) {
	ifidx, err := m.resolveIfindex(ifaceName)
	if err != nil {
		m.setIfaceState(ifaceName, DirectionIngress, StateFailed, err.Error())
		m.setIfaceState(ifaceName, DirectionEgress, StateFailed, err.Error())
		return nil, fmt.Errorf("failed to resolve ifindex for %q: %w", ifaceName, err)
	}

	var results []AttachResult

	if opts.Ingress != nil {
		res := m.attachDirection(ifaceName, ifidx, DirectionIngress, opts.Ingress)
		results = append(results, res)
	}

	if opts.Egress != nil {
		res := m.attachDirection(ifaceName, ifidx, DirectionEgress, opts.Egress)
		results = append(results, res)
	}

	return results, nil
}

func (m *AttachmentManager) attachDirection(ifaceName string, ifindex int, direction Direction, prog *ebpf.Program) AttachResult {
	result := AttachResult{
		IfaceName: ifaceName,
		Ifindex:   ifindex,
		Direction: direction,
	}

	existing := m.getDirectionState(ifaceName, direction)
	if existing != nil && existing.State == StateAttached {
		result.State = StateAttached
		return result
	}

	var attach ebpf.AttachType
	switch direction {
	case DirectionIngress:
		attach = ebpf.AttachTCXIngress
	case DirectionEgress:
		attach = ebpf.AttachTCXEgress
	default:
		attach = ebpf.AttachTCXIngress
	}

	lnk, err := link.AttachTCX(link.TCXOptions{
		Interface: ifindex,
		Program:   prog,
		Attach:    attach,
	})
	if err != nil {
		result.State = StateFailed
		result.Error = err
		m.setDirectionState(ifaceName, direction, &DirectionState{
			State:    StateFailed,
			Link:     nil,
			ErrorMsg: err.Error(),
		})
		return result
	}

	result.State = StateAttached
	m.setDirectionState(ifaceName, direction, &DirectionState{
		State:    StateAttached,
		Link:     lnk,
		ErrorMsg: "",
	})

	return result
}

func (m *AttachmentManager) DetachIface(ifaceName string) []AttachResult {
	var results []AttachResult

	for _, dir := range []Direction{DirectionIngress, DirectionEgress} {
		result := m.detachDirection(ifaceName, dir)
		results = append(results, result)
	}

	return results
}

func (m *AttachmentManager) detachDirection(ifaceName string, direction Direction) AttachResult {
	result := AttachResult{
		IfaceName: ifaceName,
		Direction: direction,
	}

	state := m.getDirectionState(ifaceName, direction)
	if state == nil || state.State != StateAttached || state.Link == nil {
		result.State = StateDetached
		return result
	}

	if err := state.Link.Close(); err != nil {
		result.State = StateFailed
		result.Error = err
		m.setDirectionState(ifaceName, direction, &DirectionState{
			State:    StateFailed,
			Link:     nil,
			ErrorMsg: err.Error(),
		})
		return result
	}

	result.State = StateDetached
	m.setDirectionState(ifaceName, direction, &DirectionState{
		State:    StateDetached,
		Link:     nil,
		ErrorMsg: "",
	})

	return result
}

func (m *AttachmentManager) DetachAll() []AttachResult {
	var results []AttachResult
	for ifaceName := range m.ifaceStates {
		res := m.DetachIface(ifaceName)
		results = append(results, res...)
	}
	return results
}

func (m *AttachmentManager) resolveIfindex(ifaceName string) (int, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return 0, fmt.Errorf("interface %q not found: %w", ifaceName, err)
	}
	return iface.Index, nil
}

func (m *AttachmentManager) setIfaceState(ifaceName string, direction Direction, state AttachState, errorMsg string) {
	ifaceState, ok := m.ifaceStates[ifaceName]
	if !ok {
		ifaceState = &IfaceState{
			Name:    ifaceName,
			Ingress: &DirectionState{},
			Egress:  &DirectionState{},
		}
		m.ifaceStates[ifaceName] = ifaceState
	}

	switch direction {
	case DirectionIngress:
		ifaceState.Ingress = &DirectionState{
			State:    state,
			Link:     ifaceState.Ingress.Link,
			ErrorMsg: errorMsg,
		}
		if state == StateDetached {
			ifaceState.Ingress.Link = nil
		}
	case DirectionEgress:
		ifaceState.Egress = &DirectionState{
			State:    state,
			Link:     ifaceState.Egress.Link,
			ErrorMsg: errorMsg,
		}
		if state == StateDetached {
			ifaceState.Egress.Link = nil
		}
	}
}

func (m *AttachmentManager) setDirectionState(ifaceName string, direction Direction, state *DirectionState) {
	ifaceState, ok := m.ifaceStates[ifaceName]
	if !ok {
		ifaceState = &IfaceState{
			Name:    ifaceName,
			Ingress: &DirectionState{},
			Egress:  &DirectionState{},
		}
		m.ifaceStates[ifaceName] = ifaceState
	}

	switch direction {
	case DirectionIngress:
		ifaceState.Ingress = state
	case DirectionEgress:
		ifaceState.Egress = state
	}
}

func (m *AttachmentManager) getDirectionState(ifaceName string, direction Direction) *DirectionState {
	ifaceState, ok := m.ifaceStates[ifaceName]
	if !ok {
		return nil
	}

	switch direction {
	case DirectionIngress:
		return ifaceState.Ingress
	case DirectionEgress:
		return ifaceState.Egress
	default:
		return nil
	}
}

func (m *AttachmentManager) GetIfaceState(ifaceName string) *IfaceState {
	return m.ifaceStates[ifaceName]
}

func (m *AttachmentManager) GetAllIfaceStates() map[string]*IfaceState {
	return m.ifaceStates
}

func (m *AttachmentManager) IsIfaceAttached(ifaceName string) bool {
	ifaceState := m.GetIfaceState(ifaceName)
	if ifaceState == nil {
		return false
	}
	ingressOK := ifaceState.Ingress != nil && ifaceState.Ingress.State == StateAttached
	egressOK := ifaceState.Egress != nil && ifaceState.Egress.State == StateAttached
	return ingressOK || egressOK
}

func (m *AttachmentManager) GetTrafficMap() *TrafficMap {
	return m.trafficMap
}

func (m *AttachmentManager) SetCollection(coll *ebpf.Collection, tm *TrafficMap) {
	m.collection = coll
	m.trafficMap = tm
}

func (m *AttachmentManager) GetIngress(ifaceName string) *ebpf.Program {
	if m.collection == nil {
		return nil
	}
	return m.collection.Programs["handle_ingress"]
}

func (m *AttachmentManager) GetEgress(ifaceName string) *ebpf.Program {
	if m.collection == nil {
		return nil
	}
	return m.collection.Programs["handle_egress"]
}

func (m *AttachmentManager) IsMockMode() bool {
	if m.collection == nil {
		return true
	}
	ingress := m.collection.Programs["handle_ingress"]
	egress := m.collection.Programs["handle_egress"]
	return ingress == nil && egress == nil
}

func (m *AttachmentManager) GetAttachedInterfaces() []string {
	var attached []string
	for name, state := range m.ifaceStates {
		if state.Ingress != nil && state.Ingress.State == StateAttached {
			attached = append(attached, name)
		}
	}
	return attached
}

func (m *AttachmentManager) GetFailedInterfaces() []string {
	var failed []string
	for name, state := range m.ifaceStates {
		if (state.Ingress != nil && state.Ingress.State == StateFailed) ||
			(state.Egress != nil && state.Egress.State == StateFailed) {
			failed = append(failed, name)
		}
	}
	return failed
}
