package ebpf

import (
	"fmt"

	"github.com/cilium/ebpf"
)

type TrafficMap struct {
	m *ebpf.Map
}

func NewTrafficMap() (*TrafficMap, error) {
	mp, err := ebpf.NewMap(&TrafficMapSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to create traffic map: %w", err)
	}
	return &TrafficMap{m: mp}, nil
}

func (tm *TrafficMap) Map() *ebpf.Map {
	return tm.m
}

func (tm *TrafficMap) Lookup(key *TrafficKey) (*TrafficCounter, error) {
	var val TrafficCounter
	if err := tm.m.Lookup(key, &val); err != nil {
		return nil, err
	}
	return &val, nil
}

func (tm *TrafficMap) NextKey(prev *TrafficKey) (*TrafficKey, error) {
	var next TrafficKey
	if err := tm.m.NextKey(prev, &next); err != nil {
		return nil, err
	}
	return &next, nil
}

func (tm *TrafficMap) Iterate() *MapIterator {
	return &MapIterator{tm: tm}
}

type MapIterator struct {
	tm      *TrafficMap
	currKey *TrafficKey
	iterErr error
}

func (mi *MapIterator) Next() (bool, *TrafficKey, *TrafficCounter) {
	if mi.iterErr != nil {
		return false, nil, nil
	}
	key, err := mi.tm.NextKey(mi.currKey)
	if err != nil {
		mi.iterErr = err
		return false, nil, nil
	}
	counter, err := mi.tm.Lookup(key)
	if err != nil {
		mi.iterErr = err
		return false, nil, nil
	}
	mi.currKey = key
	return true, key, counter
}
