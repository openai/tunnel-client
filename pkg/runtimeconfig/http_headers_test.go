package runtimeconfig

import "testing"

func TestValidHTTPHeader(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		valid bool
	}{
		{"X-Test", "value", true},
		{"X-Test", "", true},
		{"X-Test", "value\t\x80\xff", true},
		{"!#$%&'*+-.^_`|~012abcXYZ", "value", true},
		{"", "value", false},
		{"Bad Header", "value", false},
		{"Bad:Header", "value", false},
		{"Bad\r\nHeader", "value", false},
		{"Bad\x80Header", "value", false},
		{"X-Test", "value\r\nInjected: yes", false},
		{"X-Test", "value\r", false},
		{"X-Test", "value\n", false},
		{"X-Test", "value\x00", false},
		{"X-Test", "value\x1f", false},
		{"X-Test", "value\x7f", false},
	} {
		if got := ValidHTTPHeader(tc.name, tc.value); got != tc.valid {
			t.Errorf("ValidHTTPHeader(%q, %q) = %t; want %t", tc.name, tc.value, got, tc.valid)
		}
	}
}
