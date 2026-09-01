package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Prompter abstracts interactive prompts for testability.
type Prompter interface {
	Line(label, def string) string
	Choose(label string, options []string, def string) string
	Bool(label string, def bool) bool
}

// StdPrompter reads from stdin via bufio.Scanner.
type StdPrompter struct {
	scanner *bufio.Scanner
	out     io.Writer
}

// NewStdPrompter creates a Prompter reading from in and writing prompts to out.
func NewStdPrompter(in io.Reader, out io.Writer) *StdPrompter {
	return &StdPrompter{
		scanner: bufio.NewScanner(in),
		out:     out,
	}
}

func (p *StdPrompter) Line(label, def string) string {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	if !p.scanner.Scan() {
		return def
	}
	text := strings.TrimSpace(p.scanner.Text())
	if text == "" {
		return def
	}
	return text
}

func (p *StdPrompter) Choose(label string, options []string, def string) string {
	for {
		if len(options) > 0 {
			fmt.Fprintf(p.out, "%s (%s) [%s]: ", label, strings.Join(options, "/"), def)
		} else {
			fmt.Fprintf(p.out, "%s [%s]: ", label, def)
		}
		if !p.scanner.Scan() {
			return def
		}
		text := strings.TrimSpace(p.scanner.Text())
		if text == "" {
			return def
		}
		// Validate against options if provided.
		if len(options) == 0 {
			return text
		}
		for _, opt := range options {
			if text == opt {
				return text
			}
		}
		fmt.Fprintf(p.out, "invalid choice %q, expected one of %v\n", text, options)
	}
}

func (p *StdPrompter) Bool(label string, def bool) bool {
	defStr := "y/N"
	if def {
		defStr = "Y/n"
	}
	fmt.Fprintf(p.out, "%s (%s): ", label, defStr)
	if !p.scanner.Scan() {
		return def
	}
	text := strings.TrimSpace(strings.ToLower(p.scanner.Text()))
	if text == "" {
		return def
	}
	switch text {
	case "y", "yes", "true", "1":
		return true
	case "n", "no", "false", "0":
		return false
	default:
		// Unrecognized input -> treat as def for simplicity; could reprompt.
		return def
	}
}

// ScriptedPrompter returns predetermined answers for tests.
type ScriptedPrompter struct {
	Values []string
	idx    int
}

// NewScriptedPrompter creates a scripted prompter with sequential answers.
func NewScriptedPrompter(values []string) *ScriptedPrompter {
	return &ScriptedPrompter{Values: values}
}

func (p *ScriptedPrompter) next(def string) string {
	if p.idx >= len(p.Values) {
		return def
	}
	val := p.Values[p.idx]
	p.idx++
	if val == "" {
		return def
	}
	return val
}

func (p *ScriptedPrompter) Line(label, def string) string {
	return p.next(def)
}

func (p *ScriptedPrompter) Choose(label string, options []string, def string) string {
	val := p.next(def)
	// If options present and val not in options, return val anyway (wizard will validate and reprompt).
	return val
}

func (p *ScriptedPrompter) Bool(label string, def bool) bool {
	val := p.next("")
	if val == "" {
		return def
	}
	lower := strings.ToLower(strings.TrimSpace(val))
	switch lower {
	case "y", "yes", "true", "1":
		return true
	case "n", "no", "false", "0":
		return false
	default:
		// For scripted bools, treat "y"/"yes" as true, anything else as false.
		// If the test passes "true"/"false" explicitly, handle.
		if lower == "true" {
			return true
		}
		if lower == "false" {
			return false
		}
		return def
	}
}
