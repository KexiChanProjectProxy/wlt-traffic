package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/kexi/traffic-count/internal/bootstrap"
	"github.com/kexi/traffic-count/internal/config"
	"github.com/kexi/traffic-count/internal/runtime"
	"github.com/kexi/traffic-count/internal/storage"
)

type mockService struct {
	healthy   bool
	degraded  bool
	failed    bool
	attached  []string
	failedIfs []string
	status    *runtime.Status
}

func (m *mockService) IsHealthy() bool            { return m.healthy }
func (m *mockService) IsDegraded() bool           { return m.degraded }
func (m *mockService) IsFailed() bool             { return m.failed }
func (m *mockService) GetStatus() *runtime.Status { return m.status }

type mockFlushLoop struct {
	lastFlush time.Time
	flushErr  error
	stale     bool
}

func (m *mockFlushLoop) LastFlush() time.Time { return m.lastFlush }
func (m *mockFlushLoop) FlushError() error    { return m.flushErr }
func (m *mockFlushLoop) IsStale() bool        { return m.stale }

// Helper to create a test server with mocks
func newTestServer(cfg *config.Config, ms *mockService, mfl *mockFlushLoop, repo *storage.Repository) *Server {
	return &Server{
		cfg:   cfg,
		mux:   http.NewServeMux(),
		svc:   ms,
		repo:  repo,
		flush: mfl,
	}
}

func TestHandleHealthz(t *testing.T) {
	tests := []struct {
		name           string
		healthy        bool
		degraded       bool
		failed         bool
		stale          bool
		expectedStatus int
	}{
		{
			name:           "healthy and not stale",
			healthy:        true,
			degraded:       false,
			failed:         false,
			stale:          false,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "healthy but stale",
			healthy:        true,
			degraded:       false,
			failed:         false,
			stale:          true,
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "degraded",
			healthy:        false,
			degraded:       true,
			failed:         false,
			stale:          false,
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "failed",
			healthy:        false,
			degraded:       false,
			failed:         true,
			stale:          false,
			expectedStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &mockService{
				healthy:  tt.healthy,
				degraded: tt.degraded,
				failed:   tt.failed,
			}
			mfl := &mockFlushLoop{stale: tt.stale}

			cfg := config.New()
			srv := newTestServer(cfg, ms, mfl, nil)

			req := httptest.NewRequest("GET", "/healthz", nil)
			w := httptest.NewRecorder()
			srv.handleHealthz(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("handleHealthz() status = %d, want %d", w.Code, tt.expectedStatus)
			}
		})
	}
}

func TestHandleStatus(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_status_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	tests := []struct {
		name         string
		healthy      bool
		degraded     bool
		failed       bool
		stale        bool
		flushErr     error
		expectedMode string
	}{
		{
			name:         "healthy mode",
			healthy:      true,
			degraded:     false,
			failed:       false,
			stale:        false,
			flushErr:     nil,
			expectedMode: "healthy",
		},
		{
			name:         "degraded mode",
			healthy:      false,
			degraded:     true,
			failed:       false,
			stale:        false,
			flushErr:     nil,
			expectedMode: "degraded",
		},
		{
			name:         "failed mode",
			healthy:      false,
			degraded:     false,
			failed:       true,
			stale:        false,
			flushErr:     nil,
			expectedMode: "failed",
		},
		{
			name:         "healthy but stale becomes degraded",
			healthy:      true,
			degraded:     false,
			failed:       false,
			stale:        true,
			flushErr:     nil,
			expectedMode: "degraded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &mockService{
				healthy:  tt.healthy,
				degraded: tt.degraded,
				failed:   tt.failed,
				status: &runtime.Status{
					Mode:               getMode(tt.healthy, tt.degraded, tt.failed),
					AttachedInterfaces: []string{"lo"},
					FailedInterfaces:   []string{},
				},
			}
			mfl := &mockFlushLoop{
				lastFlush: time.Unix(1744567800, 0),
				stale:     tt.stale,
				flushErr:  tt.flushErr,
			}

			cfg := config.New()
			cfg.Interfaces = []string{"lo"}
			srv := newTestServer(cfg, ms, mfl, repo)

			req := httptest.NewRequest("GET", "/api/v1/status", nil)
			w := httptest.NewRecorder()
			srv.handleStatus(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("handleStatus() status = %d, want %d", w.Code, http.StatusOK)
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshaling response: %v", err)
			}

			if resp["mode"] != tt.expectedMode {
				t.Errorf("mode = %q, want %q", resp["mode"], tt.expectedMode)
			}
		})
	}
}

func getMode(healthy, degraded, failed bool) bootstrap.Mode {
	if healthy {
		return bootstrap.ModeHealthy
	}
	if degraded {
		return bootstrap.ModeDegraded
	}
	return bootstrap.ModeFailed
}

func TestHandleTraffic_InvalidWindow(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_traffic_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	tests := []struct {
		name    string
		window  string
		wantErr bool
	}{
		{"valid today", "today", false},
		{"valid all", "all", false},
		{"valid 7days", "7days", false},
		{"valid 30days", "30days", false},
		{"valid month", "month", false},
		{"empty defaults to today", "", false},
		{"invalid foo", "foo", true},
		{"invalid TODAY (uppercase)", "TODAY", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/v1/traffic?window=" + tt.window
			req := httptest.NewRequest("GET", url, nil)
			w := httptest.NewRecorder()
			srv.handleTraffic(w, req)

			if tt.wantErr && w.Code != http.StatusBadRequest {
				t.Errorf("handleTraffic() status = %d for window %q, want %d", w.Code, tt.window, http.StatusBadRequest)
			}
			if !tt.wantErr && w.Code != http.StatusOK {
				t.Errorf("handleTraffic() status = %d for window %q, want %d", w.Code, tt.window, http.StatusOK)
			}
		})
	}
}

func TestHandleTraffic_InvalidMAC(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_mac_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	tests := []struct {
		name    string
		mac     string
		wantErr bool
	}{
		{"valid lowercase", "aa:bb:cc:dd:ee:ff", false},
		{"valid uppercase", "AA:BB:CC:DD:EE:FF", false},
		{"valid mixed", "Aa:Bb:Cc:Dd:Ee:Ff", false},
		{"invalid empty", "", false}, // empty is allowed (no filter)
		{"invalid short", "aa:bb:cc:dd:ee", true},
		{"invalid long", "aa:bb:cc:dd:ee:ff:00", true},
		{"invalid chars", "gg:hh:ii:jj:kk:ll", true},
		{"invalid format", "aabbccddeeff", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/v1/traffic?window=today"
			if tt.mac != "" {
				url += "&mac=" + tt.mac
			}
			req := httptest.NewRequest("GET", url, nil)
			w := httptest.NewRecorder()
			srv.handleTraffic(w, req)

			if tt.wantErr && w.Code != http.StatusBadRequest {
				t.Errorf("handleTraffic() for mac %q status = %d, want %d", tt.mac, w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestHandleTraffic_LimitOffset(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_limit_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	tests := []struct {
		name          string
		limitStr      string
		offsetStr     string
		expectedLimit int
	}{
		{"default limit", "", "", 100},
		{"valid limit 50", "50", "", 50},
		{"valid limit 1000", "1000", "", 1000},
		{"limit 0 uses default", "0", "", 100},
		{"negative limit uses default", "-1", "", 100},
		{"non-numeric uses default", "abc", "", 100},
		{"valid offset", "50", "10", 50},
		{"offset 0", "50", "0", 50},
		{"negative offset uses 0", "50", "-5", 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/v1/traffic?window=today"
			if tt.limitStr != "" {
				url += "&limit=" + tt.limitStr
			}
			if tt.offsetStr != "" {
				url += "&offset=" + tt.offsetStr
			}
			req := httptest.NewRequest("GET", url, nil)
			w := httptest.NewRecorder()
			srv.handleTraffic(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("handleTraffic() status = %d, want %d", w.Code, http.StatusOK)
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshaling response: %v", err)
			}

			gotLimit := int(resp["limit"].(float64))
			if gotLimit != tt.expectedLimit {
				t.Errorf("limit = %d, want %d", gotLimit, tt.expectedLimit)
			}
		})
	}
}

func TestMacToString(t *testing.T) {
	tests := []struct {
		name string
		mac  [6]byte
		want string
	}{
		{"all zeros", [6]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, "00:00:00:00:00:00"},
		{"all fs", [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, "ff:ff:ff:ff:ff:ff"},
		{"mixed", [6]byte{0xde, 0xad, 0xbe, 0xef, 0xfe, 0xed}, "de:ad:be:ef:fe:ed"},
		{"example", [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}, "aa:bb:cc:dd:ee:ff"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := macToString(tt.mac)
			if got != tt.want {
				t.Errorf("macToString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRuntimeContext(t *testing.T) {
	ctx := runtimeContext()
	if ctx == nil {
		t.Error("runtimeContext() returned nil")
	}
	// Should be usable for cancellation
	select {
	case <-ctx.Done():
		t.Error("context should not be done immediately")
	default:
	}
}

func TestServerNewServer(t *testing.T) {
	cfg := config.New()
	srv := NewServer(cfg, nil, nil, nil)

	if srv == nil {
		t.Fatal("NewServer returned nil")
	}

	if srv.mux == nil {
		t.Error("mux is nil")
	}

	if srv.cfg != cfg {
		t.Error("cfg not set correctly")
	}
}

func TestHandleTraffic_AllWindow(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_all_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	key := &storage.RecordKey{
		InterfaceName: "lo",
		Ifindex:       1,
		MAC:           mac,
	}

	delta := &storage.TrafficCounters{
		Bytes:          1000,
		Packets:        10,
		IngressBytes:   500,
		IngressPackets: 5,
		EgressBytes:    500,
		EgressPackets:  5,
	}

	if err := repo.UpsertTotal(ctx, key, delta); err != nil {
		t.Fatalf("upserting total: %v", err)
	}

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	req := httptest.NewRequest("GET", "/api/v1/traffic?window=all", nil)
	w := httptest.NewRecorder()
	srv.handleTraffic(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("handleTraffic() status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}

	if resp["window"] != "all" {
		t.Errorf("window = %q, want %q", resp["window"], "all")
	}

	records, ok := resp["records"].([]interface{})
	if !ok {
		t.Fatal("records is not an array")
	}

	if len(records) != 1 {
		t.Errorf("len(records) = %d, want 1", len(records))
	}
}

func TestHandleTraffic_TodayWindow(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_today_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	mac := [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}
	key := &storage.RecordKey{
		InterfaceName: "lo",
		Ifindex:       1,
		MAC:           mac,
	}

	today := storage.CurrentDateUTC()
	delta := &storage.TrafficCounters{
		Bytes:          500,
		Packets:        5,
		IngressBytes:   250,
		IngressPackets: 2,
		EgressBytes:    250,
		EgressPackets:  3,
	}

	if err := repo.UpsertDaily(ctx, key, today, delta); err != nil {
		t.Fatalf("upserting daily: %v", err)
	}

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	req := httptest.NewRequest("GET", "/api/v1/traffic?window=today", nil)
	w := httptest.NewRecorder()
	srv.handleTraffic(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("handleTraffic() status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}

	if resp["window"] != "today" {
		t.Errorf("window = %q, want %q", resp["window"], "today")
	}
}

func TestHandleTraffic_7DaysWindow(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_7days_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	req := httptest.NewRequest("GET", "/api/v1/traffic?window=7days", nil)
	w := httptest.NewRecorder()
	srv.handleTraffic(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("handleTraffic() status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}

	if resp["window"] != "7days" {
		t.Errorf("window = %q, want %q", resp["window"], "7days")
	}
}

func TestHandleTraffic_30DaysWindow(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_30days_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	req := httptest.NewRequest("GET", "/api/v1/traffic?window=30days", nil)
	w := httptest.NewRecorder()
	srv.handleTraffic(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("handleTraffic() status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}

	if resp["window"] != "30days" {
		t.Errorf("window = %q, want %q", resp["window"], "30days")
	}
}

func TestHandleTraffic_MonthWindow(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_month_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	req := httptest.NewRequest("GET", "/api/v1/traffic?window=month", nil)
	w := httptest.NewRecorder()
	srv.handleTraffic(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("handleTraffic() status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}

	if resp["window"] != "month" {
		t.Errorf("window = %q, want %q", resp["window"], "month")
	}
}

func TestHandleTraffic_WithInterfaceFilter(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_iface_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}

	for _, iface := range []string{"eth0", "eth1"} {
		key := &storage.RecordKey{
			InterfaceName: iface,
			Ifindex:       1,
			MAC:           mac,
		}
		delta := &storage.TrafficCounters{Bytes: 1000, Packets: 10}
		if err := repo.UpsertTotal(ctx, key, delta); err != nil {
			t.Fatalf("upserting total for %s: %v", iface, err)
		}
	}

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	req := httptest.NewRequest("GET", "/api/v1/traffic?window=all&interface=eth0", nil)
	w := httptest.NewRecorder()
	srv.handleTraffic(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("handleTraffic() status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}

	records, ok := resp["records"].([]interface{})
	if !ok {
		t.Fatal("records is not an array")
	}

	// Should only return eth0 records
	if len(records) != 1 {
		t.Errorf("len(records) = %d, want 1 (only eth0)", len(records))
	}
}

func TestHandleTraffic_WithMACFilter(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_macfilter_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()

	mac1 := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x01}
	mac2 := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x02}

	for _, mac := range []struct {
		addr  [6]byte
		iface string
	}{
		{mac1, "eth0"},
		{mac2, "eth0"},
	} {
		key := &storage.RecordKey{
			InterfaceName: mac.iface,
			Ifindex:       1,
			MAC:           mac.addr,
		}
		delta := &storage.TrafficCounters{Bytes: 1000, Packets: 10}
		if err := repo.UpsertTotal(ctx, key, delta); err != nil {
			t.Fatalf("upserting total: %v", err)
		}
	}

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	// Filter by MAC - case insensitive
	req := httptest.NewRequest("GET", "/api/v1/traffic?window=all&mac=AA:BB:CC:DD:EE:01", nil)
	w := httptest.NewRecorder()
	srv.handleTraffic(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("handleTraffic() status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}

	records, ok := resp["records"].([]interface{})
	if !ok {
		t.Fatal("records is not an array")
	}

	// Should only return the filtered MAC
	if len(records) != 1 {
		t.Errorf("len(records) = %d, want 1", len(records))
	}
}

func TestHandleTraffic_EmptyDatabase(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_empty_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	req := httptest.NewRequest("GET", "/api/v1/traffic?window=all", nil)
	w := httptest.NewRecorder()
	srv.handleTraffic(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("handleTraffic() status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}

	if resp == nil {
		t.Fatal("response is nil")
	}

	records, ok := resp["records"]
	if !ok {
		t.Log("records key not found in response, which is acceptable for empty result")
		return
	}

	if records == nil {
		t.Log("records is null (empty result)")
		return
	}

	recordsSlice, ok := records.([]interface{})
	if !ok {
		t.Fatalf("records is not an array, got type %T", records)
	}

	if len(recordsSlice) != 0 {
		t.Errorf("len(records) = %d, want 0 (empty database)", len(recordsSlice))
	}
}

func TestValidWindows(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_windows_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	valid := []string{"all", "today", "7days", "30days", "month", ""}
	for _, window := range valid {
		url := "/api/v1/traffic?window=" + window
		req := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		srv.handleTraffic(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("window %q: got status %d, want %d", window, w.Code, http.StatusOK)
		}
	}

	invalid := []string{"foo", "ALL", "Today", "1month"}
	for _, window := range invalid {
		url := "/api/v1/traffic?window=" + window
		req := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		srv.handleTraffic(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("window %q: got status %d, want %d", window, w.Code, http.StatusBadRequest)
		}
	}
}

func TestMACRegex(t *testing.T) {
	validMACs := []string{
		"00:00:00:00:00:00",
		"ff:ff:ff:ff:ff:ff",
		"aa:bb:cc:dd:ee:ff",
		"AA:BB:CC:DD:EE:FF",
		"01:23:45:67:89:ab",
	}

	invalidMACs := []string{
		"",
		"aa:bb:cc:dd:ee",
		"aa:bb:cc:dd:ee:ff:00",
		"gg:hh:ii:jj:kk:ll",
		"aabbccddeeff",
		"aa:bb:cc:dd:ee:ff ",
		" aa:bb:cc:dd:ee:ff",
	}

	for _, mac := range validMACs {
		if !macRegex.MatchString(mac) {
			t.Errorf("MAC %q should be valid", mac)
		}
	}

	for _, mac := range invalidMACs {
		if macRegex.MatchString(mac) {
			t.Errorf("MAC %q should be invalid", mac)
		}
	}
}

func TestServerStart(t *testing.T) {
	cfg := config.New()
	cfg.BindAddress = "127.0.0.1:0" // Use port 0 to find available port

	srv := NewServer(cfg, nil, nil, nil)

	// Start server in goroutine
	go func() {
		if err := srv.Start(); err != nil {
			// Expected if port is in use or other issues
		}
	}()

	// Give server time to start
	// Just verify it doesn't panic
}

func TestContentType(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_ctype_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ms := &mockService{healthy: true}
	mfl := &mockFlushLoop{stale: false}
	cfg := config.New()
	srv := newTestServer(cfg, ms, mfl, repo)

	req := httptest.NewRequest("GET", "/api/v1/traffic?window=all", nil)
	w := httptest.NewRecorder()
	srv.handleTraffic(w, req)

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", contentType, "application/json")
	}
}
