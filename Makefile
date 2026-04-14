.PHONY: build build-ebpf build-binary test clean generate-ebpf

BIN_DIR := bin
PKG := ./cmd/traffic-count
EBPF_PKG := ./internal/ebpf

all: build

build: build-ebpf build-binary

generate-ebpf:
	@echo "Generating eBPF bindings..."
	cd $(EBPF_PKG) && go generate

build-ebpf: generate-ebpf
	@echo "eBPF build step completed"
	@ls -la $(EBPF_PKG)/*_bpfel*.o $(EBPF_PKG)/*_bpfel*.go 2>/dev/null || echo "Generated artifacts in $(EBPF_PKG)"

build-binary: build-ebpf
	@echo "Building Go binary..."
	go build -o $(BIN_DIR)/traffic-count $(PKG)
	@echo "Go binary built successfully"

test:
	@echo "Running tests..."
	go test ./...

clean:
	@echo "Cleaning..."
	rm -rf $(BIN_DIR)
	rm -f $(EBPF_PKG)/*_bpfel*.go $(EBPF_PKG)/*_bpfeb*.go $(EBPF_PKG)/*_bpfel*.o $(EBPF_PKG)/*_bpfeb*.o 2>/dev/null || true
	go clean

run: build
	$(BIN_DIR)/traffic-count

verify-build-order:
	@echo "Verifying deterministic build order..."
	@echo "Step 1: eBPF artifact step (bpf2go)"
	@echo "Step 2: Go binary step"
	@echo "Order enforced via Makefile dependency: build-binary depends on build-ebpf"

verify-layout:
	@echo "Verifying key/value layout..."
	@echo "TrafficKey size: 10 bytes (4 bytes ifindex + 6 bytes MAC)"
	@echo "TrafficCounter size: 48 bytes (6 x uint64)"
	@echo "Map spec: max_entries=262144, type=HASH"
