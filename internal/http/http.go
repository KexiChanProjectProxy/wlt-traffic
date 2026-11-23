// Package http serves the read-only JSON API.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kexi/traffic-count/internal/ebpf"
	"github.com/kexi/traffic-count/internal/runtime"
	"github.com/kexi/traffic-count/internal/storage"
)

// Status is the live state the API reports.
type Status interface {
	Interfaces() []ebpf.IfaceStatus
	LastFlush() time.Time
	FlushError() error
	Live() []runtime.LiveRate
}

type Server struct {
	repo          *storage.Repository
	st            Status
	loc           *time.Location
	flushInterval time.Duration
	configured    []string
	now           func() time.Time
	mux           *http.ServeMux
}

func NewServer(repo *storage.Repository, st Status, loc *time.Location, flushInterval time.Duration, configured []string) *Server {
	s := &Server{repo: repo, st: st, loc: loc, flushInterval: flushInterval, configured: configured, now: time.Now, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/v1/traffic", s.handleTraffic)
	s.mux.HandleFunc("GET /api/v1/summary", s.handleSummary)
	s.mux.HandleFunc("GET /api/v1/series", s.handleSeries)
	s.mux.HandleFunc("GET /api/v1/live", s.handleLive)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

// ---- health / status ----

func (s *Server) healthy() (bool, string) {
	attached := 0
	for _, i := range s.st.Interfaces() {
		if i.Attached {
			attached++
		}
	}
	switch {
	case attached == 0:
		return false, "failed"
	case s.st.FlushError() != nil || s.now().Sub(s.st.LastFlush()) > 3*s.flushInterval:
		return false, "degraded"
	case attached < len(s.configured):
		return false, "degraded"
	}
	return true, "healthy"
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if ok, _ := s.healthy(); ok {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	_, mode := s.healthy()
	ifaces := s.st.Interfaces()
	var attached, failed []string
	for _, i := range ifaces {
		if i.Attached {
			attached = append(attached, i.Name)
		} else {
			failed = append(failed, i.Name)
		}
	}
	flushErr := ""
	if err := s.st.FlushError(); err != nil {
		flushErr = err.Error()
	}
	var lastFlush int64
	if t := s.st.LastFlush(); !t.IsZero() {
		lastFlush = t.Unix()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":                 mode,
		"interfaces":           ifaces,
		"configured_ifaces":    s.configured,
		"attached_ifaces":      nonNil(attached),
		"failed_ifaces":        nonNil(failed),
		"last_flush_timestamp": lastFlush,
		"flush_error":          flushErr,
		"timezone":             s.loc.String(),
		"today":                s.now().In(s.loc).Format(time.DateOnly),
	})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- windows ----

// windowRange returns the inclusive day range for a window name. All-time
// returns empty strings.
func (s *Server) windowRange(window string) (from, to string, err error) {
	now := s.now().In(s.loc)
	today := now.Format(time.DateOnly)
	day := func(t time.Time) string { return t.Format(time.DateOnly) }
	switch window {
	case "", "today":
		return today, today, nil
	case "yesterday":
		y := day(now.AddDate(0, 0, -1))
		return y, y, nil
	case "7days", "week":
		return day(now.AddDate(0, 0, -6)), today, nil
	case "30days":
		return day(now.AddDate(0, 0, -29)), today, nil
	case "month":
		return day(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, s.loc)), today, nil
	case "lastmonth":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, s.loc)
		return day(first.AddDate(0, -1, 0)), day(first.AddDate(0, 0, -1)), nil
	case "all":
		return "", "", nil
	}
	return "", "", fmt.Errorf("invalid window %q, must be one of: today, yesterday, 7days, 30days, month, lastmonth, all", window)
}

func normMAC(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	hw, err := net.ParseMAC(v)
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("invalid mac %q", v)
	}
	return hw.String(), nil
}

// TrafficRecord is one row of /api/v1/traffic.
//
// upload = sent by the device, download = received by the device.
// ingress_* / egress_* are the same numbers from the LAN interface's point of
// view (ingress == upload, egress == download), kept for older clients.
type TrafficRecord struct {
	Interface       string   `json:"interface"`
	Interfaces      []string `json:"interfaces,omitempty"`
	MAC             string   `json:"mac"`
	UploadBytes     uint64   `json:"upload_bytes"`
	DownloadBytes   uint64   `json:"download_bytes"`
	TotalBytes      uint64   `json:"total_bytes"`
	UploadPackets   uint64   `json:"upload_packets"`
	DownloadPackets uint64   `json:"download_packets"`
	TotalPackets    uint64   `json:"total_packets"`
	IngressBytes    uint64   `json:"ingress_bytes"`
	EgressBytes     uint64   `json:"egress_bytes"`
	IngressPackets  uint64   `json:"ingress_packets"`
	EgressPackets   uint64   `json:"egress_packets"`
	LastSeen        int64    `json:"last_seen"`
	Window          string   `json:"window"`
}

func toRecord(r storage.Row, window string) TrafficRecord {
	c := r.Counters
	return TrafficRecord{
		Interface: r.Iface, Interfaces: r.Ifaces, MAC: r.MAC,
		UploadBytes: c.UpBytes, DownloadBytes: c.DownBytes, TotalBytes: c.UpBytes + c.DownBytes,
		UploadPackets: c.UpPackets, DownloadPackets: c.DownPackets, TotalPackets: c.UpPackets + c.DownPackets,
		IngressBytes: c.UpBytes, EgressBytes: c.DownBytes, IngressPackets: c.UpPackets, EgressPackets: c.DownPackets,
		LastSeen: r.LastSeen, Window: window,
	}
}

func intParam(r *http.Request, name string, def, min, max int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, min, max)
	}
	return n, nil
}

func ifaceParam(r *http.Request) string {
	q := r.URL.Query()
	if v := q.Get("interface"); v != "" {
		return v
	}
	return q.Get("iface")
}

// GET /api/v1/traffic?window=&mac=&interface=&group=mac|interface&sort=total|upload|download|mac&limit=&offset=
func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	window := q.Get("window")
	if window == "" {
		window = "today"
	}
	from, to, err := s.windowRange(window)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	mac, err := normMAC(q.Get("mac"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	group := q.Get("group")
	if group != "" && group != "mac" && group != "interface" {
		badRequest(w, "group must be mac or interface")
		return
	}
	sortBy := q.Get("sort")
	switch sortBy {
	case "", "total", "upload", "download", "mac":
	default:
		badRequest(w, "sort must be one of: total, upload, download, mac")
		return
	}
	limit, err := intParam(r, "limit", 100, 1, 1000)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	offset, err := intParam(r, "offset", 0, 0, 1<<30)
	if err != nil {
		badRequest(w, err.Error())
		return
	}

	rows, count, err := s.repo.QueryTraffic(r.Context(), storage.Query{
		From: from, To: to, MAC: mac, Iface: ifaceParam(r),
		GroupByMAC: group == "mac", Sort: sortBy, Limit: limit, Offset: offset,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	records := make([]TrafficRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, toRecord(row, window))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"window":      window,
		"from":        from,
		"to":          to,
		"timezone":    s.loc.String(),
		"limit":       limit,
		"offset":      offset,
		"total_count": count,
		"records":     records,
	})
}

// WindowTotals is one window of /api/v1/summary.
type WindowTotals struct {
	From          string `json:"from,omitempty"`
	To            string `json:"to,omitempty"`
	UploadBytes   uint64 `json:"upload_bytes"`
	DownloadBytes uint64 `json:"download_bytes"`
	TotalBytes    uint64 `json:"total_bytes"`
}

var summaryWindows = []string{"today", "yesterday", "7days", "30days", "month", "lastmonth", "all"}

// GET /api/v1/summary?mac=[&interface=] -> totals for every window in one call.
// Without mac, sums all devices.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	mac, err := normMAC(r.URL.Query().Get("mac"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	iface := ifaceParam(r)
	out := map[string]any{"mac": mac, "timezone": s.loc.String()}
	windows := map[string]WindowTotals{}
	for _, win := range summaryWindows {
		from, to, _ := s.windowRange(win)
		rows, _, err := s.repo.QueryTraffic(r.Context(), storage.Query{From: from, To: to, MAC: mac, Iface: iface, GroupByMAC: true})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		t := WindowTotals{From: from, To: to}
		for _, row := range rows {
			t.UploadBytes += row.Counters.UpBytes
			t.DownloadBytes += row.Counters.DownBytes
		}
		t.TotalBytes = t.UploadBytes + t.DownloadBytes
		windows[win] = t
	}
	out["windows"] = windows
	if mac != "" {
		for _, l := range s.st.Live() {
			if l.MAC == mac {
				out["live"] = l
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/v1/series?mac=&days=30[&interface=] -> daily points, oldest first.
func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	mac, err := normMAC(r.URL.Query().Get("mac"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	days, err := intParam(r, "days", 30, 1, 400)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	now := s.now().In(s.loc)
	to := now.Format(time.DateOnly)
	from := now.AddDate(0, 0, -(days - 1)).Format(time.DateOnly)
	points, err := s.repo.Series(r.Context(), mac, ifaceParam(r), from, to)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	type point struct {
		Date          string `json:"date"`
		UploadBytes   uint64 `json:"upload_bytes"`
		DownloadBytes uint64 `json:"download_bytes"`
		TotalBytes    uint64 `json:"total_bytes"`
	}
	out := make([]point, 0, len(points))
	for _, p := range points {
		out = append(out, point{p.Day, p.Counters.UpBytes, p.Counters.DownBytes, p.Counters.UpBytes + p.Counters.DownBytes})
	}
	writeJSON(w, http.StatusOK, map[string]any{"mac": mac, "from": from, "to": to, "timezone": s.loc.String(), "days": out})
}

// GET /api/v1/live[?mac=] -> throughput over the last flush interval (bytes/s).
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	mac, err := normMAC(r.URL.Query().Get("mac"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	devices := []runtime.LiveRate{}
	for _, l := range s.st.Live() {
		if mac == "" || strings.EqualFold(l.MAC, mac) {
			devices = append(devices, l)
		}
	}
	var sampled int64
	if t := s.st.LastFlush(); !t.IsZero() {
		sampled = t.Unix()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"interval_seconds": int(s.flushInterval.Seconds()),
		"sampled_at":       sampled,
		"devices":          devices,
	})
}

// ListenAndServe serves until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}
