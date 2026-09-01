package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestScriptedPrompter_Line(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		def    string
		want   string
	}{
		{"returns first value", []string{"hello"}, "", "hello"},
		{"uses def on empty string", []string{""}, "default", "default"},
		{"uses def when exhausted", []string{}, "fallback", "fallback"},
		{"sequential", []string{"first", "second"}, "", "first"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewScriptedPrompter(tc.values)
			got := p.Line("label", tc.def)
			if got != tc.want {
				t.Errorf("Line = %q want %q", got, tc.want)
			}
		})
	}
}

func TestScriptedPrompter_Choose(t *testing.T) {
	p := NewScriptedPrompter([]string{"local"})
	got := p.Choose("Source", []string{"local", "external"}, "local")
	if got != "local" {
		t.Fatalf("Choose = %q want local", got)
	}
	p2 := NewScriptedPrompter([]string{""})
	got = p2.Choose("Source", []string{"local", "external"}, "external")
	if got != "external" {
		t.Fatalf("empty Choose should return def, got %q", got)
	}
}

func TestScriptedPrompter_Bool(t *testing.T) {
	tests := []struct {
		name string
		val  string
		def  bool
		want bool
	}{
		{"y true", "y", false, true},
		{"yes true", "yes", false, true},
		{"true true", "true", false, true},
		{"n false", "n", true, false},
		{"no false", "no", true, false},
		{"empty def true", "", true, true},
		{"empty def false", "", false, false},
		{"1 true", "1", false, true},
		{"0 false", "0", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewScriptedPrompter([]string{tc.val})
			got := p.Bool("label", tc.def)
			if got != tc.want {
				t.Errorf("Bool(%q, %v) = %v want %v", tc.val, tc.def, got, tc.want)
			}
		})
	}
}

func TestStdPrompter_Line(t *testing.T) {
	in := strings.NewReader("hello\n")
	var out bytes.Buffer
	p := NewStdPrompter(in, &out)
	got := p.Line("Name", "def")
	if got != "hello" {
		t.Fatalf("Std Line = %q want hello", got)
	}
	if !strings.Contains(out.String(), "Name") {
		t.Fatalf("prompt not written: %q", out.String())
	}
}

func TestStdPrompter_Bool(t *testing.T) {
	in := strings.NewReader("y\n")
	var out bytes.Buffer
	p := NewStdPrompter(in, &out)
	if !p.Bool("Enable?", false) {
		t.Fatal("expected true")
	}
	in2 := strings.NewReader("\n")
	var out2 bytes.Buffer
	p2 := NewStdPrompter(in2, &out2)
	if p2.Bool("Enable?", true) != true {
		t.Fatal("empty should return def true")
	}
}
