package runtime

import "testing"

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		item     string
		expected bool
	}{
		{"empty slice", []string{}, "foo", false},
		{"single match", []string{"foo"}, "foo", true},
		{"single no match", []string{"bar"}, "foo", false},
		{"multiple match first", []string{"foo", "bar", "baz"}, "foo", true},
		{"multiple match middle", []string{"foo", "bar", "baz"}, "bar", true},
		{"multiple match last", []string{"foo", "bar", "baz"}, "baz", true},
		{"multiple no match", []string{"foo", "bar", "baz"}, "qux", false},
		{"nil slice", nil, "foo", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contains(tt.slice, tt.item)
			if got != tt.expected {
				t.Errorf("contains(%v, %q) = %v, want %v", tt.slice, tt.item, got, tt.expected)
			}
		})
	}
}

func TestHasFailedInterface(t *testing.T) {
	tests := []struct {
		name     string
		iface    string
		failed   []string
		expected bool
	}{
		{"not in failed list", "eth0", []string{"eth1"}, false},
		{"in failed list", "eth0", []string{"eth0"}, true},
		{"in failed list among others", "eth0", []string{"eth1", "eth0", "eth2"}, true},
		{"empty failed list", "eth0", []string{}, false},
		{"nil failed list", "eth0", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasFailedInterface(tt.iface, tt.failed)
			if got != tt.expected {
				t.Errorf("hasFailedInterface(%q, %v) = %v, want %v", tt.iface, tt.failed, got, tt.expected)
			}
		})
	}
}

func TestStringsContains(t *testing.T) {
	tests := []struct {
		name     string
		haystack []string
		needle   string
		expected bool
	}{
		{"exact match", []string{"foo", "bar"}, "foo", true},
		{"exact no match", []string{"foo", "bar"}, "baz", false},
		{"substring via comma", []string{"foo", "bar"}, "o", true},
		{"substring via comma no match", []string{"foo", "bar"}, "z", false},
		{"empty haystack", []string{}, "foo", false},
		{"nil haystack", nil, "foo", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringsContains(tt.haystack, tt.needle)
			if got != tt.expected {
				t.Errorf("stringsContains(%v, %q) = %v, want %v", tt.haystack, tt.needle, got, tt.expected)
			}
		})
	}
}
