package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kexi/traffic-count/internal/config"
	"github.com/kexi/traffic-count/internal/runtime"
	"github.com/kexi/traffic-count/internal/storage"
)

var macRegex = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

type ServiceHealth interface {
	IsHealthy() bool
	IsDegraded() bool
	IsFailed() bool
	GetStatus() *runtime.Status
}

type FlushLoopStatus interface {
	LastFlush() time.Time
	FlushError() error
	IsStale() bool
}

type Server struct {
	cfg   *config.Config
	mux   *http.ServeMux
	svc   ServiceHealth
	repo  *storage.Repository
	flush FlushLoopStatus
}

func NewServer(cfg *config.Config, svc ServiceHealth, repo *storage.Repository, flush FlushLoopStatus) *Server {
	mux := http.NewServeMux()
	s := &Server{cfg: cfg, mux: mux, svc: svc, repo: repo, flush: flush}

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	mux.HandleFunc("GET /api/v1/traffic", s.handleTraffic)

	return s
}

func (s *Server) Start() error {
	addr := s.cfg.BindAddress
	return http.ListenAndServe(addr, s.mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if s.svc.IsHealthy() && !s.flush.IsStale() {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := s.svc.GetStatus()

	flushTime := int64(0)
	if !s.flush.LastFlush().IsZero() {
		flushTime = s.flush.LastFlush().Unix()
	}

	flushErrStr := ""
	if err := s.flush.FlushError(); err != nil {
		flushErrStr = err.Error()
	}

	mode := "healthy"
	if s.svc.IsDegraded() {
		mode = "degraded"
	} else if s.svc.IsFailed() {
		mode = "failed"
	}

	if s.flush.IsStale() && mode == "healthy" {
		mode = "degraded"
	}

	resp := map[string]interface{}{
		"mode":                 mode,
		"configured_ifaces":    s.cfg.Interfaces,
		"attached_ifaces":      status.AttachedInterfaces,
		"failed_ifaces":        status.FailedInterfaces,
		"last_flush_timestamp": flushTime,
		"flush_error":          flushErrStr,
		"database_path":        s.cfg.DatabasePath,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	window := query.Get("window")
	if window == "" {
		window = "today"
	}

	validWindows := map[string]bool{"all": true, "30days": true, "7days": true, "today": true, "month": true}
	if !validWindows[window] {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid window, must be one of: all, 30days, 7days, today, month"})
		return
	}

	ifaceFilter := query.Get("interface")
	macFilter := query.Get("mac")

	if macFilter != "" && !macRegex.MatchString(macFilter) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid mac format, must be XX:XX:XX:XX:XX:XX"})
		return
	}

	limit := 100
	if limitStr := query.Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 1000 {
			limit = l
		}
	}

	offset := 0
	if offsetStr := query.Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	var records []TrafficRecord
	var err error

	switch window {
	case "all":
		records, err = s.queryAll(ifaceFilter, macFilter, limit, offset)
	case "today":
		records, err = s.queryToday(ifaceFilter, macFilter, limit, offset)
	case "7days":
		records, err = s.queryDays(7, ifaceFilter, macFilter, limit, offset)
	case "30days":
		records, err = s.queryDays(30, ifaceFilter, macFilter, limit, offset)
	case "month":
		records, err = s.queryMonth(ifaceFilter, macFilter, limit, offset)
	}

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"window":  window,
		"limit":   limit,
		"offset":  offset,
		"records": records,
	})
}

type TrafficRecord struct {
	Interface      string `json:"interface"`
	Ifindex        uint32 `json:"ifindex"`
	MAC            string `json:"mac"`
	IngressBytes   uint64 `json:"ingress_bytes"`
	EgressBytes    uint64 `json:"egress_bytes"`
	TotalBytes     uint64 `json:"total_bytes"`
	IngressPackets uint64 `json:"ingress_packets"`
	EgressPackets  uint64 `json:"egress_packets"`
	TotalPackets   uint64 `json:"total_packets"`
	Window         string `json:"window"`
}

func macToString(mac [6]byte) string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}

func (s *Server) queryAll(ifaceFilter, macFilter string, limit, offset int) ([]TrafficRecord, error) {
	totals, err := s.repo.ListTotals(runtimeContext(), ifaceFilter)
	if err != nil {
		return nil, err
	}

	var records []TrafficRecord
	count := 0
	skip := 0

	for _, t := range totals {
		macStr := macToString(t.MAC)
		if macFilter != "" && !strings.EqualFold(macStr, macFilter) {
			continue
		}

		if skip < offset {
			skip++
			continue
		}

		if count >= limit {
			break
		}

		records = append(records, TrafficRecord{
			Interface:      t.InterfaceName,
			Ifindex:        t.Ifindex,
			MAC:            macStr,
			IngressBytes:   t.IngressBytes,
			EgressBytes:    t.EgressBytes,
			TotalBytes:     t.Bytes,
			IngressPackets: t.IngressPackets,
			EgressPackets:  t.EgressPackets,
			TotalPackets:   t.Packets,
			Window:         "all",
		})
		count++
	}

	return records, nil
}

func (s *Server) queryToday(ifaceFilter, macFilter string, limit, offset int) ([]TrafficRecord, error) {
	today := storage.CurrentDateUTC()
	return s.queryDateRange(today, today, ifaceFilter, macFilter, limit, offset, "today")
}

func (s *Server) queryDays(days int, ifaceFilter, macFilter string, limit, offset int) ([]TrafficRecord, error) {
	end := storage.CurrentDateUTC()
	start := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -days+1))
	windowName := fmt.Sprintf("%ddays", days)
	return s.queryDateRange(start, end, ifaceFilter, macFilter, limit, offset, windowName)
}

func (s *Server) queryMonth(ifaceFilter, macFilter string, limit, offset int) ([]TrafficRecord, error) {
	now := time.Now().UTC()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := storage.CurrentDateUTC()
	start := storage.FormatDateUTC(startOfMonth)
	return s.queryDateRange(start, end, ifaceFilter, macFilter, limit, offset, "month")
}

func (s *Server) queryDateRange(start, end, ifaceFilter, macFilter string, limit, offset int, windowName string) ([]TrafficRecord, error) {
	daily, err := s.repo.ListDaily(runtimeContext(), start, end, ifaceFilter)
	if err != nil {
		return nil, err
	}

	type aggKey struct {
		iface string
		ifidx uint32
		mac   [6]byte
	}
	aggregated := make(map[aggKey]*TrafficRecord)

	for _, d := range daily {
		macStr := macToString(d.MAC)
		if macFilter != "" && !strings.EqualFold(macStr, macFilter) {
			continue
		}

		key := aggKey{iface: d.InterfaceName, ifidx: d.Ifindex, mac: d.MAC}
		if rec, ok := aggregated[key]; ok {
			rec.IngressBytes += d.IngressBytes
			rec.EgressBytes += d.EgressBytes
			rec.TotalBytes += d.Bytes
			rec.IngressPackets += d.IngressPackets
			rec.EgressPackets += d.EgressPackets
			rec.TotalPackets += d.Packets
		} else {
			aggregated[key] = &TrafficRecord{
				Interface:      d.InterfaceName,
				Ifindex:        d.Ifindex,
				MAC:            macStr,
				IngressBytes:   d.IngressBytes,
				EgressBytes:    d.EgressBytes,
				TotalBytes:     d.Bytes,
				IngressPackets: d.IngressPackets,
				EgressPackets:  d.EgressPackets,
				TotalPackets:   d.Packets,
				Window:         windowName,
			}
		}
	}

	var records []TrafficRecord
	count := 0
	skip := 0

	for _, rec := range aggregated {
		if skip < offset {
			skip++
			continue
		}
		if count >= limit {
			break
		}
		records = append(records, *rec)
		count++
	}

	return records, nil
}

func runtimeContext() context.Context {
	return context.Background()
}
