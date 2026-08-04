package main

import "testing"

func TestParseReviewResponseArgs(t *testing.T) {
	handle, rationale, err := parseReviewResponseArgs([]string{"a1b2c3d4e5", "-m", "migration plan reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	if handle != "a1b2c3d4e5" || rationale != "migration plan reviewed" {
		t.Fatalf("parsed = %q, %q", handle, rationale)
	}
	for _, args := range [][]string{
		{},
		{"a1b2c3d4e5"},
		{"a1b2c3d4e5", "-m", ""},
		{"a1b2c3d4e5", "--message", "reason"},
	} {
		if _, _, err := parseReviewResponseArgs(args); err == nil {
			t.Fatalf("parse %q succeeded, want error", args)
		}
	}
}
