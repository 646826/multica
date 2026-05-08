package adapter

import (
	"context"
	"strings"
	"testing"
)

func TestProgramBenchCatalog_Kind(t *testing.T) {
	c := NewProgramBenchCatalog()
	if c.Kind() != "programbench" {
		t.Fatalf("Kind() = %q, want %q", c.Kind(), "programbench")
	}
}

func TestProgramBenchComposer_Compose(t *testing.T) {
	c := NewProgramBenchComposer()
	out, err := c.Compose(context.Background(), ComposeInput{
		Run:  RunRef{DisplayName: "smoke-1"},
		Task: TaskRef{InstanceID: "abishekvashok__cmatrix.5c082c6"},
		Instance: Instance{
			ID:         "abishekvashok__cmatrix.5c082c6",
			Language:   "c",
			Difficulty: "easy",
		},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if out.SubmissionFilename != "submission.tar.gz" {
		t.Fatalf("SubmissionFilename = %q", out.SubmissionFilename)
	}
	if out.AssigneeAgentName != "ProgramBenchRunner" {
		t.Fatalf("AssigneeAgentName = %q", out.AssigneeAgentName)
	}
	if !strings.Contains(out.Description, "abishekvashok__cmatrix.5c082c6") {
		t.Fatalf("Description missing instance id")
	}
	if !strings.Contains(out.Description, "abishekvashok_1776_cmatrix.5c082c6") {
		t.Fatalf("Description missing cleanroom image (__→_1776_ rule)")
	}
	if !strings.Contains(out.Description, "compile.sh") {
		t.Fatalf("Description missing compile.sh contract")
	}
	if !strings.Contains(strings.ToLower(out.Description), "internet") {
		t.Fatalf("Description missing internet ban")
	}
	if !strings.Contains(strings.ToLower(out.Title), "smoke-1") {
		t.Fatalf("Title missing run display name; got %q", out.Title)
	}
}

func TestProgramBenchParser_Validate(t *testing.T) {
	p := NewProgramBenchParser()
	if err := p.Validate(context.Background(), Attachment{
		Filename: "submission.tar.gz", MimeType: "application/gzip", SizeBytes: 1 << 20,
	}); err != nil {
		t.Fatalf("Validate(valid): %v", err)
	}
	if err := p.Validate(context.Background(), Attachment{
		Filename: "wrong.zip", MimeType: "application/zip", SizeBytes: 1 << 20,
	}); err == nil {
		t.Fatal("Validate(wrong filename): want error")
	}
	if err := p.Validate(context.Background(), Attachment{
		Filename: "submission.tar.gz", MimeType: "application/gzip", SizeBytes: 1 << 31,
	}); err == nil {
		t.Fatal("Validate(too large): want error")
	}
}
