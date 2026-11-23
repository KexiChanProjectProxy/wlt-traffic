.PHONY: all build generate test e2e deb clean

BIN := bin/traffic-count

all: build

# Regenerates internal/ebpf/traffic_bpfel.{o,go} from bpf/traffic.c.
# Needs clang and libbpf-dev. The generated files are committed, so a plain
# `go build` works without them.
generate:
	cd internal/ebpf && go generate

build:
	CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/traffic-count

test:
	go test ./...

# Real kernel test in throw-away network namespaces (root required).
e2e: build
	sudo scripts/e2e-netns.sh $(BIN)
	sudo TRAFFIC_COUNT_FORCE_TC=1 scripts/e2e-netns.sh $(BIN)

deb:
	debian/rules binary

clean:
	rm -rf bin debian/tmp
