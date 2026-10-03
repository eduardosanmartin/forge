package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eduardosanmartin/forge/internal/llmjson"
)

// Verifier checks a task's DESCRIPTIVE done criteria (no "cmd:" prefix)
// after the task ran (RNF-8.3: "done" needs positive verification, not
// just the absence of an error). It returns whether the criteria are met
// and the evidence offered; err reports a verifier failure.
type Verifier func(ctx context.Context, task Task) (met bool, evidence string, err error)

// Verdict is the JSON a verifier model must answer with.
type Verdict struct {
	Met      bool   `json:"met"`
	Evidence string `json:"evidence"`
}

// BuildVerificationPrompt asks the model that did the task to check its
// own work against the declared criteria — with tools available, so it can
// LOOK (read the files, run the tests) instead of asserting from memory.
func BuildVerificationPrompt(task Task) string {
	return fmt.Sprintf(`VERIFICATION STEP for task %s — do not change any file in this step.

Done criteria declared for the task:
%s

Check whether the criteria are met NOW, by inspecting the actual state (read the files, run read-only checks) rather than recalling what you intended. Then answer with ONLY this JSON object:
{"met": true or false, "evidence": "the concrete fact you checked that proves it, or what is missing"}`, task.ID, strings.TrimSpace(task.DoneCriteria))
}

// ParseVerdict extracts a Verdict from a model reply, tolerating code
// fences and surrounding prose. An unparsable reply is an error: an
// unreadable verdict must never count as "met".
func ParseVerdict(raw string) (Verdict, error) {
	candidates := llmjson.Candidates(raw)
	if i, j := strings.IndexByte(raw, '{'), strings.LastIndexByte(raw, '}'); i >= 0 && j > i {
		candidates = append(candidates, raw[i:j+1])
	}
	for _, c := range candidates {
		var v Verdict
		var probe map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(c)), &probe) != nil {
			continue
		}
		if _, ok := probe["met"].(bool); !ok {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(c)), &v); err == nil {
			return v, nil
		}
	}
	return Verdict{}, fmt.Errorf("verifier reply is not a {\"met\": bool, \"evidence\": ...} JSON object: %q", tailForReport(strings.TrimSpace(raw), 300))
}
