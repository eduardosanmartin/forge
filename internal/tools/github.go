package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// githubIssueListFields/githubIssueViewFields etc. pin the exact --json
// fields requested per subcommand so output stays predictable regardless of
// the installed gh version's own defaults.
const (
	githubIssueListFields = "number,title,state,author,labels,createdAt,updatedAt,url"
	githubIssueViewFields = "number,title,state,author,body,labels,comments,createdAt,updatedAt,url"
	githubPRListFields    = "number,title,state,author,headRefName,baseRefName,createdAt,updatedAt,url"
	githubPRViewFields    = "number,title,state,author,body,headRefName,baseRefName,createdAt,updatedAt,url,reviews,statusCheckRollup"
)

// githubTool implements a read-only GitHub issues/PRs tool (RF-10.3) by
// shelling out to the user's own `gh` CLI — reusing its stored auth rather
// than managing a token ourselves. It recognizes exactly four subcommands,
// each a fixed, hardcoded `gh ... --json ...` invocation: there is no path
// from tool arguments to a mutating gh command (create/close/merge/...).
// Permission-gated by KindGitHub (internal/perms), deny-by-default like
// git/shell — not the KindCustom floor, since this reaches the network.
type githubTool struct{}

func newGitHubTool() *githubTool { return &githubTool{} }

func (t *githubTool) Name() string { return "github" }
func (t *githubTool) Description() string {
	return "Read GitHub issues and pull requests for a repository via the `gh` CLI (read-only: list/view only, never creates/modifies anything). Requires `gh` installed and authenticated (`gh auth login`)."
}

func (t *githubTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subcommand": map[string]any{
				"type":        "string",
				"enum":        []string{"issue-list", "issue-view", "pr-list", "pr-view"},
				"description": "Which read to perform.",
			},
			"number": map[string]any{
				"type":        "number",
				"description": "Issue or PR number. Required for issue-view/pr-view, ignored otherwise.",
			},
			"repo": map[string]any{
				"type":        "string",
				"description": "Optional \"owner/repo\" override. OMIT to use the repository in the current workspace.",
			},
			"limit": map[string]any{
				"type":        "number",
				"description": "Max results for issue-list/pr-list (default 30).",
			},
			"state": map[string]any{
				"type":        "string",
				"enum":        []string{"open", "closed", "all"},
				"description": "Filter by state for issue-list/pr-list (default open).",
			},
			"search": map[string]any{
				"type":        "string",
				"description": "Optional search query for issue-list/pr-list (gh's search syntax).",
			},
		},
		"required": []string{"subcommand"},
	}
}

func (t *githubTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	ghArgs, err := buildGitHubArgs(req.Subcommand, req.Input)
	if err != nil {
		return Result{Content: "ERROR: " + err.Error()}, nil
	}

	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(execCtx, "gh", ghArgs...)
	output, err := cmd.CombinedOutput()

	timedOut := execCtx.Err() == context.DeadlineExceeded
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if timedOut {
			exitCode = -1
		} else {
			exitCode = -2
		}
	}

	metadata := map[string]any{
		"exit_code":  exitCode,
		"subcommand": req.Subcommand,
	}
	if timedOut {
		metadata["timeout"] = true
	}

	return Result{Content: string(output), Metadata: metadata}, nil
}

// buildGitHubArgs translates one (subcommand, input) pair into the exact gh
// CLI argv to run. Pulled out of Execute as a pure function so the argument
// logic — the only part with real branching — is testable without actually
// invoking gh or touching the network.
func buildGitHubArgs(subcommand string, input map[string]any) ([]string, error) {
	repo, _ := input["repo"].(string)
	var args []string

	switch subcommand {
	case "issue-list":
		args = []string{"issue", "list", "--json", githubIssueListFields}
		args = append(args, githubListFilters(input)...)
	case "issue-view":
		number, err := requireGitHubNumber(input)
		if err != nil {
			return nil, err
		}
		args = []string{"issue", "view", number, "--json", githubIssueViewFields}
	case "pr-list":
		args = []string{"pr", "list", "--json", githubPRListFields}
		args = append(args, githubListFilters(input)...)
	case "pr-view":
		number, err := requireGitHubNumber(input)
		if err != nil {
			return nil, err
		}
		args = []string{"pr", "view", number, "--json", githubPRViewFields}
	default:
		return nil, fmt.Errorf("unknown github subcommand %q", subcommand)
	}

	if repo != "" {
		args = append(args, "--repo", repo)
	}
	return args, nil
}

// githubListFilters translates the optional limit/state/search input fields
// into gh flags, shared by issue-list and pr-list.
func githubListFilters(input map[string]any) []string {
	var args []string
	if limit, ok := input["limit"].(float64); ok && limit > 0 {
		args = append(args, "--limit", strconv.Itoa(int(limit)))
	}
	if state, ok := input["state"].(string); ok && state != "" {
		args = append(args, "--state", state)
	}
	if search, ok := input["search"].(string); ok && search != "" {
		args = append(args, "--search", search)
	}
	return args
}

// requireGitHubNumber extracts and stringifies the "number" input field,
// required by issue-view/pr-view.
func requireGitHubNumber(input map[string]any) (string, error) {
	n, ok := input["number"].(float64)
	if !ok || n <= 0 {
		return "", fmt.Errorf("number is required and must be a positive issue/PR number")
	}
	return strconv.Itoa(int(n)), nil
}
