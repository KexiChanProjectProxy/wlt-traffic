package config

import (
	"strings"
	"testing"
)

func TestParseUsesSnakeCaseKeys(t *testing.T) {
	cfg := Default()
	err := Parse([]byte(`{"interfaces":["br-lan"],"bind_address":"127.0.0.1:9999","database_path":"/x.db",
		"flush_interval":5,"timezone":"Asia/Shanghai","allow_partial":true,"log_level":"debug"}`), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindAddress != "127.0.0.1:9999" || cfg.DatabasePath != "/x.db" || cfg.FlushInterval != 5 || !cfg.AllowPartial {
		t.Fatalf("keys not applied: %+v", cfg)
	}
	if cfg.Location().String() != "Asia/Shanghai" {
		t.Fatalf("timezone: %v", cfg.Location())
	}
}

func TestParseRejects(t *testing.T) {
	for _, tc := range []struct{ json, want string }{
		{`{"interfaces":[]}`, "interfaces"},
		{`{"interfaces":["a","a"]}`, "twice"},
		{`{"interfaces":["a"],"bind_address":"0.0.0.0:1"}`, "loopback"},
		{`{"interfaces":["a"],"flush_interval":0}`, "flush_interval"},
		{`{"interfaces":["a"],"timezone":"Mars/Base"}`, "timezone"},
		{`{"interfaces":["a"],"bind_adress":"127.0.0.1:1"}`, "unknown field"},
	} {
		err := Parse([]byte(tc.json), Default())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", tc.json, err, tc.want)
		}
	}
}
