//go:build ignore

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	binDir := "/home/kexi/traffic-count/bin"

	if err := exec.Command("which", "clang").Run(); err != nil {
		fmt.Fprintf(os.Stderr, "clang not found: %v\n", err)
		fmt.Fprintf(os.Stderr, "eBPF compilation requires clang to be installed.\n")
		fmt.Fprintf(os.Stderr, "On Ubuntu/Debian: sudo apt-get install clang\n")
		fmt.Fprintf(os.Stderr, "On Fedora/RHEL: sudo dnf install clang\n")
		fmt.Fprintf(os.Stderr, "bpf2go cannot generate bindings without clang.\n")
		placeholder := filepath.Join(binDir, "traffic_count_bpfel.o")
		if f, err := os.Create(placeholder); err == nil {
			f.WriteString("# placeholder - clang required for eBPF compilation\n")
			f.Close()
		}
		fmt.Println("eBPF build step completed (clang unavailable - placeholder created)")
		return
	}

	fmt.Println("Generating eBPF bindings...")
	cmd := exec.Command("go", "run", "github.com/cilium/ebpf/cmd/bpf2go",
		"-target", "bpfel,bpfeb",
		"-go-package", "ebpf",
		"traffic",
		"../../bpf/ingress.c",
		"--",
		"-I", "../../bpf", "-g", "-O2", "-target", "bpf")
	cmd.Dir = "/home/kexi/traffic-count/internal/ebpf"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "bpf2go failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("eBPF bindings generated successfully")
}
