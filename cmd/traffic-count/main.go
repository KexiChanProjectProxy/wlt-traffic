// SPDX-License-Identifier: GPL-2.0 OR BSD-2-Clause

// traffic-count accounts per-device (per MAC) LAN traffic with eBPF and serves
// it over a loopback JSON API.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kexi/traffic-count/internal/config"
	"github.com/kexi/traffic-count/internal/ebpf"
	apihttp "github.com/kexi/traffic-count/internal/http"
	"github.com/kexi/traffic-count/internal/runtime"
	"github.com/kexi/traffic-count/internal/storage"
)

type status struct {
	*runtime.Accountant
	col *ebpf.Collector
}

func (s status) Interfaces() []ebpf.IfaceStatus { return s.col.Status() }

func main() {
	log.SetFlags(0) // journald adds timestamps
	configPath := flag.String("config", "/etc/traffic-count/config.json", "path to the JSON config file")
	flag.Parse()
	if err := run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	loc := cfg.Location()

	repo, err := storage.New(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer repo.Close()

	col, err := ebpf.Load()
	if err != nil {
		return err
	}
	defer col.Close()

	var failed []string
	for _, name := range cfg.Interfaces {
		if err := col.Attach(name); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failed) == len(cfg.Interfaces) || (len(failed) > 0 && !cfg.AllowPartial) {
		return fmt.Errorf("attach failed (allow_partial=%v): %s", cfg.AllowPartial, strings.Join(failed, "; "))
	}
	for _, s := range col.Status() {
		if s.Attached {
			log.Printf("attached to %s (ifindex %d, %s)", s.Name, s.Ifindex, s.Mode)
		} else {
			log.Printf("not attached to %s: %s (will retry when it appears)", s.Name, s.Error)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	acct := runtime.NewAccountant(col, repo, loc)
	acctDone := make(chan struct{})
	go func() {
		acct.Run(ctx, time.Duration(cfg.LiveInterval)*time.Second, time.Duration(cfg.FlushInterval)*time.Second)
		close(acctDone)
	}()
	go col.Watch(ctx, cfg.Interfaces, nil)
	go runtime.Housekeep(ctx, repo, loc, cfg.RetentionDays, time.Duration(cfg.HousekeepingInterval)*time.Second, log.Printf)

	srv := apihttp.NewServer(repo, status{acct, col}, loc, time.Duration(cfg.FlushInterval)*time.Second, cfg.Interfaces)
	srv.LiveInterval = time.Duration(cfg.LiveInterval) * time.Second
	log.Printf("API listening on %s (days counted in %s)", cfg.BindAddress, loc)
	err = srv.ListenAndServe(ctx, cfg.BindAddress)
	stop()
	<-acctDone // final flush
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("http server: %w", err)
	}
	log.Printf("shutdown complete")
	return nil
}
