package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kexi/traffic-count/internal/ebpf"
	"github.com/kexi/traffic-count/internal/runtime"
	"github.com/kexi/traffic-count/internal/storage"
)

type fakeStatus struct{ last time.Time }

func (f fakeStatus) Interfaces() []ebpf.IfaceStatus {
	return []ebpf.IfaceStatus{{Name: "br-lan", Ifindex: 2, Attached: true, Mode: "tcx"}}
}
func (f fakeStatus) LastFlush() time.Time { return f.last }
func (f fakeStatus) FlushError() error    { return nil }
func (f fakeStatus) Live() []runtime.LiveRate {
	return []runtime.LiveRate{{MAC: "aa:00:00:00:00:01", UploadBps: 10, DownloadBps: 20}}
}

func newServer(t *testing.T) *Server {
	t.Helper()
	repo, err := storage.New(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, loc)
	repo.AddDeltas(context.Background(), []storage.Delta{
		{"2026-11-23", "br-lan", "aa:00:00:00:00:01", storage.Counters{100, 1, 900, 9}},
		{"2026-11-23", "br-lan", "aa:00:00:00:00:02", storage.Counters{5000, 5, 5000, 5}},
		{"2026-11-20", "br-lan", "aa:00:00:00:00:01", storage.Counters{1, 1, 1, 1}},
		{"2026-10-31", "br-lan", "aa:00:00:00:00:01", storage.Counters{7, 1, 7, 1}},
	}, now)
	s := NewServer(repo, fakeStatus{last: now}, loc, 10*time.Second, []string{"br-lan"})
	s.now = func() time.Time { return now }
	return s
}

func get(t *testing.T, s *Server, url string, wantCode int) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != wantCode {
		t.Fatalf("%s: code %d, want %d: %s", url, rec.Code, wantCode, rec.Body)
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func TestTrafficSortedWithDirections(t *testing.T) {
	s := newServer(t)
	out := get(t, s, "/api/v1/traffic?window=today", 200)
	recs := out["records"].([]any)
	if len(recs) != 2 || out["total_count"].(float64) != 2 {
		t.Fatalf("records: %v", out)
	}
	first := recs[0].(map[string]any)
	second := recs[1].(map[string]any)
	if first["mac"] != "aa:00:00:00:00:02" {
		t.Fatalf("not sorted by total: %v", recs)
	}
	if second["upload_bytes"].(float64) != 100 || second["download_bytes"].(float64) != 900 ||
		second["ingress_bytes"].(float64) != 100 || second["egress_bytes"].(float64) != 900 {
		t.Fatalf("directions wrong: %v", second)
	}
	out = get(t, s, "/api/v1/traffic?window=7days&mac=AA-00-00-00-00-01&iface=br-lan", 200)
	if r := out["records"].([]any)[0].(map[string]any); r["total_bytes"].(float64) != 1002 {
		t.Fatalf("7days filter: %v", r)
	}
	get(t, s, "/api/v1/traffic?window=bogus", 400)
	get(t, s, "/api/v1/traffic?mac=nope", 400)
	get(t, s, "/api/v1/traffic?limit=0", 400)
}

func TestSummarySeriesLiveStatus(t *testing.T) {
	s := newServer(t)
	out := get(t, s, "/api/v1/summary?mac=aa:00:00:00:00:01", 200)
	w := out["windows"].(map[string]any)
	if w["today"].(map[string]any)["total_bytes"].(float64) != 1000 ||
		w["month"].(map[string]any)["total_bytes"].(float64) != 1002 ||
		w["lastmonth"].(map[string]any)["total_bytes"].(float64) != 14 ||
		w["all"].(map[string]any)["total_bytes"].(float64) != 1016 {
		t.Fatalf("summary: %v", w)
	}
	if out["live"].(map[string]any)["download_bps"].(float64) != 20 {
		t.Fatalf("live in summary: %v", out)
	}
	ser := get(t, s, "/api/v1/series?mac=aa:00:00:00:00:01&days=7", 200)
	days := ser["days"].([]any)
	if len(days) != 7 || days[6].(map[string]any)["date"] != "2026-11-23" || days[3].(map[string]any)["total_bytes"].(float64) != 2 {
		t.Fatalf("series: %v", days)
	}
	live := get(t, s, "/api/v1/live", 200)
	if len(live["devices"].([]any)) != 1 {
		t.Fatalf("live: %v", live)
	}
	st := get(t, s, "/api/v1/status", 200)
	if st["mode"] != "healthy" || st["today"] != "2026-11-23" {
		t.Fatalf("status: %v", st)
	}
	get(t, s, "/healthz", 200)
}
