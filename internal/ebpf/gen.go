// Package ebpf loads the per-device traffic counters into the kernel, attaches
// them to LAN interfaces and reads them back.
//
// The BPF object is compiled at development time with `go generate` (needs
// clang and libbpf-dev) and embedded into the binary, so targets need no
// compiler. It is little-endian only (amd64, arm64, ...).
package ebpf

//go:generate sh -c "go run github.com/cilium/ebpf/cmd/bpf2go -target bpfel -go-package ebpf traffic ../../bpf/traffic.c -- -O2 -g -Wall -Werror -I/usr/include/$(gcc -print-multiarch)"
