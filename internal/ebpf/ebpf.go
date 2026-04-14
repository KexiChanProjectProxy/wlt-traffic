package ebpf

import (
	"github.com/cilium/ebpf"
)

type TrafficKey struct {
	Ifindex uint32
	Mac     [6]byte
}

type TrafficCounter struct {
	Bytes          uint64
	Packets        uint64
	IngressBytes   uint64
	IngressPackets uint64
	EgressBytes    uint64
	EgressPackets  uint64
}

var TrafficMapSpec = ebpf.MapSpec{
	Name:       "traffic_map",
	Type:       ebpf.Hash,
	KeySize:    12,
	ValueSize:  48,
	MaxEntries: 262144,
}
