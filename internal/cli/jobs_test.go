package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

func TestJobsCommandsExist(t *testing.T) {
	// forge jobs list
	jobsCmd := newJobsCommand()
	if jobsCmd.Use != "jobs" {
		t.Fatalf("jobs Use = %q", jobsCmd.Use)
	}
	found := false
	for _, c := range jobsCmd.Commands() {
		if c.Name() == "list" {
			found = true
			if c.Flags().Lookup("json") == nil {
				t.Fatalf("jobs list must expose --json")
			}
		}
	}
	if !found {
		t.Fatalf("jobs list subcommand missing")
	}

	// forge job follow/cancel
	jobCmd := newJobCommand()
	if jobCmd.Use != "job" {
		t.Fatalf("job Use = %q", jobCmd.Use)
	}
	subs := map[string]bool{}
	for _, c := range jobCmd.Commands() {
		subs[c.Name()] = true
		if c.Flags().Lookup("json") == nil {
			t.Errorf("job %q must expose --json", c.Name())
		}
	}
	for _, want := range []string{"follow", "cancel"} {
		if !subs[want] {
			t.Fatalf("missing job subcommand %q", want)
		}
	}
}

func TestJSONEnvelope_JobsList_Shape(t *testing.T) {
	var out bytes.Buffer
	fake := daemon.JobListResult{Jobs: []daemon.JobResult{{ID: "sess:1", SessionID: "sess", Seq: 1, Status: daemon.JobRunning}}}
	if err := writeJSONResultEnvelope(&out, "jobs list", &fake); err != nil {
		t.Fatalf("write: %v", err)
	}
	var env envelopeRaw
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK || env.Command != "jobs list" {
		t.Fatalf("envelope mismatch: %+v", env)
	}
	if env.Metadata["command"] != "jobs list" {
		t.Errorf("metadata.command")
	}
}

func TestJSONEnvelope_JobFollow_Shape(t *testing.T) {
	var out bytes.Buffer
	payload := map[string]any{"job": daemon.JobResult{ID: "sess:1", Status: daemon.JobDone}, "messages": []daemon.MessageResult{}}
	if err := writeJSONResultEnvelope(&out, "job follow", payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	var env envelopeRaw
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	assertEnvelopeInvariants(t, env, "job follow")
}

func TestJSONEnvelope_JobCancel_Shape(t *testing.T) {
	var out bytes.Buffer
	fake := daemon.JobCancelResult{Canceled: true, JobID: "sess:1", Status: daemon.JobCanceled}
	if err := writeJSONResultEnvelope(&out, "job cancel", &fake); err != nil {
		t.Fatalf("write: %v", err)
	}
	var env envelopeRaw
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	assertEnvelopeInvariants(t, env, "job cancel")
}
