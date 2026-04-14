// SPDX-License-Identifier: GPL-2.0 OR BSD-2-Clause
// Traffic counting service - main entrypoint

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kexi/traffic-count/internal/bootstrap"
	"github.com/kexi/traffic-count/internal/config"
	"github.com/kexi/traffic-count/internal/http"
	"github.com/kexi/traffic-count/internal/runtime"
	"github.com/kexi/traffic-count/internal/storage"
)

var (
	configPath     = flag.String("config", "", "Path to configuration file")
	ebpfObjectPath = flag.String("ebpf-object", "", "Path to eBPF object file (default: /usr/lib/traffic-count/traffic_count_bpfel.o)")
)

func main() {
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to load config from %q: %v\n", *configPath, err)
		os.Exit(1)
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	validator := bootstrap.NewValidator(cfg)
	result, err := validator.Validate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if result != nil && len(result.FailedInterfaces) > 0 {
			fmt.Fprintf(os.Stderr, "failed interfaces: %v\n", result.FailedInterfaces)
			b, _ := json.Marshal(result)
			fmt.Fprintf(os.Stderr, "startup result: %s\n", string(b))
		}
		if result != nil && result.Mode == bootstrap.ModeFailed {
			os.Exit(1)
		}
	}

	svc := runtime.NewService(cfg, *ebpfObjectPath)
	if err := svc.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to start service: %v\n", err)
		os.Exit(1)
	}
	if result != nil {
		svc.UpdateFromResult(result)
	}

	if !svc.IsHealthy() && !svc.IsDegraded() {
		fmt.Fprintf(os.Stderr, "error: service in failed state\n")
		os.Exit(1)
	}

	repo, err := storage.New(cfg.DatabasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer repo.Close()

	flush := runtime.NewFlushLoop(repo, svc.GetTrafficMap(), cfg.FlushInterval)
	if err := flush.Start(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to start flush loop: %v\n", err)
		os.Exit(1)
	}

	housekeeping := runtime.NewHousekeepingLoop(repo, cfg.HousekeepingInterval)
	if err := housekeeping.Start(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to start housekeeping loop: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		srv := http.NewServer(cfg, svc, repo, flush)
		fmt.Printf("HTTP server listening on %s\n", cfg.BindAddress)
		if err := srv.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
		}
	}()

	select {
	case sig := <-sigCh:
		fmt.Printf("Received signal %v, shutting down gracefully...\n", sig)
		cancel()
	case <-ctx.Done():
	}

	if err := housekeeping.Stop(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error during housekeeping stop: %v\n", err)
	}

	if err := flush.Stop(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error during flush stop: %v\n", err)
	}

	if err := svc.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "error during shutdown: %v\n", err)
	}
	fmt.Println("Service shutdown complete")
}
