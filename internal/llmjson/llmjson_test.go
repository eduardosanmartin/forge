package llmjson

import "testing"

func TestStripCodeFence(t *testing.T) {
	cases := []struct{ in, want string }{
		{`[{"a":1}]`, `[{"a":1}]`},
		{"```json\n[{\"a\":1}]\n```", `[{"a":1}]`},
		{"```\n[{\"a\":1}]\n```", `[{"a":1}]`},
		{"  [{\"a\":1}]  ", `[{"a":1}]`},
	}
	for _, c := range cases {
		if got := StripCodeFence(c.in); got != c.want {
			t.Errorf("StripCodeFence(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOutermostBrackets(t *testing.T) {
	t.Run("plain array", func(t *testing.T) {
		s := `[{"a":1},{"b":2}]`
		start, end, ok := OutermostBrackets(s)
		if !ok || s[start:end] != s {
			t.Fatalf("got %d:%d ok=%v, want the whole string", start, end, ok)
		}
	})

	t.Run("trailing prose with its own bracket", func(t *testing.T) {
		// A naive "first '[' to last ']'" scan would swallow the trailing
		// "arr[0]" example into the match. This must not happen.
		s := `[{"a":1}] and by the way, arr[0] is the first element`
		start, end, ok := OutermostBrackets(s)
		if !ok {
			t.Fatal("expected a match")
		}
		if s[start:end] != `[{"a":1}]` {
			t.Errorf("got %q, want %q", s[start:end], `[{"a":1}]`)
		}
	})

	t.Run("bracket inside a string value", func(t *testing.T) {
		s := `[{"text":"returns arr[0] on success"}]`
		start, end, ok := OutermostBrackets(s)
		if !ok || s[start:end] != s {
			t.Fatalf("got %d:%d ok=%v, want the whole string (bracket inside the string must not affect depth)", start, end, ok)
		}
	})

	t.Run("no array present", func(t *testing.T) {
		if _, _, ok := OutermostBrackets("no brackets here"); ok {
			t.Fatal("expected ok=false")
		}
	})
}

func TestCandidates_OrderAndContent(t *testing.T) {
	raw := "```json\n[{\"a\":1}] trailing prose\n```"
	cands := Candidates(raw)
	if len(cands) != 3 {
		t.Fatalf("got %d candidates, want 3 (raw, fence-stripped, bracket-extracted)", len(cands))
	}
	if cands[0] != raw {
		t.Errorf("candidates[0] should be the raw input unchanged")
	}
	if cands[2] != `[{"a":1}]` {
		t.Errorf("candidates[2] = %q, want the bracket-extracted array only", cands[2])
	}
}
