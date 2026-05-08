package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"text/template"
	"time"
)

const (
	programBenchKind   = "programbench"
	submissionMaxBytes = 1 << 30 // 1 GiB
)

// ----- Catalog -----

type ProgramBenchCatalog struct {
	mu      sync.Mutex
	cache   map[string]Instance
	runArgs func(ctx context.Context, args ...string) ([]byte, error)
}

func NewProgramBenchCatalog() *ProgramBenchCatalog {
	return &ProgramBenchCatalog{
		cache: map[string]Instance{},
		runArgs: func(ctx context.Context, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(cctx, args[0], args[1:]...)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Run(); err != nil {
				return out.Bytes(), fmt.Errorf("uvx: %w (output=%s)", err, strings.TrimSpace(out.String()))
			}
			return out.Bytes(), nil
		},
	}
}

func (c *ProgramBenchCatalog) Kind() string { return programBenchKind }

func (c *ProgramBenchCatalog) Resolve(ctx context.Context, instanceID string) (Instance, error) {
	c.mu.Lock()
	if cached, ok := c.cache[instanceID]; ok {
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	raw, err := c.runArgs(ctx, "uvx", "--from", "programbench", "python", "-c",
		fmt.Sprintf(`import json; from programbench import data; print(json.dumps(data.task_metadata(%q)))`, instanceID))
	if err != nil {
		return Instance{}, fmt.Errorf("programbench resolve %s: %w", instanceID, err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Instance{}, fmt.Errorf("decode metadata: %w", err)
	}
	metaJSON, _ := json.Marshal(meta)
	inst := Instance{
		ID:         instanceID,
		Language:   stringOf(meta["language"]),
		Difficulty: stringOf(meta["difficulty"]),
		Meta:       metaJSON,
	}
	c.mu.Lock()
	c.cache[instanceID] = inst
	c.mu.Unlock()
	return inst, nil
}

func (c *ProgramBenchCatalog) List(ctx context.Context, filter ListFilter) ([]Instance, error) {
	raw, err := c.runArgs(ctx, "uvx", "--from", "programbench", "python", "-c",
		`import json; from programbench import data; print(json.dumps(data.list_tasks()))`)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := []Instance{}
	for _, r := range rows {
		if filter.Language != "" && stringOf(r["language"]) != filter.Language {
			continue
		}
		if filter.Difficulty != "" && stringOf(r["difficulty"]) != filter.Difficulty {
			continue
		}
		m, _ := json.Marshal(r)
		out = append(out, Instance{
			ID:         stringOf(r["id"]),
			Language:   stringOf(r["language"]),
			Difficulty: stringOf(r["difficulty"]),
			Meta:       m,
		})
	}
	if filter.Offset > 0 && filter.Offset < len(out) {
		out = out[filter.Offset:]
	} else if filter.Offset >= len(out) {
		out = out[:0]
	}
	if filter.Limit > 0 && filter.Limit < len(out) {
		out = out[:filter.Limit]
	}
	return out, nil
}

// ----- IssueComposer -----

type ProgramBenchComposer struct {
	tpl *template.Template
}

const programBenchTemplate = "# {{.Run.DisplayName}} — {{.Task.InstanceID}}\n\n" +
	"**Instance:** `{{.Task.InstanceID}}`\n" +
	"**Cleanroom image:** `programbench/{{.CleanroomImage}}:task_cleanroom`\n" +
	"**Language:** {{.Instance.Language}}    **Difficulty:** {{.Instance.Difficulty}}\n\n" +
	"You are the **ProgramBenchRunner** agent. Solve this task in the cleanroom image, then attach a single artifact named exactly `submission.tar.gz` to a comment on this issue.\n\n" +
	"## Rules\n" +
	"- Do not use internet access while solving.\n" +
	"- Do not search the internet, package registries, forks, or mirrors for this project's source.\n" +
	"- Do not install the original project from a package manager.\n" +
	"- Do not read cached source from package caches.\n" +
	"- Do not wrap, copy, or reuse the provided executable.\n" +
	"- Do not decompile or trace (`strace`/`ltrace`) the provided executable.\n" +
	"- Infer behavior from running the executable, varying inputs, and reading provided docs.\n" +
	"- Produce a complete replacement codebase, not patches.\n\n" +
	"## Submission contract\n" +
	"Archive root must contain:\n" +
	"- `compile.sh` — chmod +x; running it must leave an executable named exactly `./executable`.\n" +
	"- Sources / build files needed for `compile.sh` to succeed.\n\n" +
	"Do **not** wrap the source in an extra directory inside the archive.\n\n" +
	"Verify locally before attaching:\n" +
	"```bash\nchmod +x ./compile.sh && ./compile.sh && test -x ./executable\n```\n"

func NewProgramBenchComposer() *ProgramBenchComposer {
	return &ProgramBenchComposer{
		tpl: template.Must(template.New("pb").Parse(programBenchTemplate)),
	}
}

func (c *ProgramBenchComposer) Kind() string { return programBenchKind }

func (c *ProgramBenchComposer) Compose(ctx context.Context, in ComposeInput) (ComposeOutput, error) {
	cleanroom := strings.ReplaceAll(in.Task.InstanceID, "__", "_1776_")
	var buf bytes.Buffer
	if err := c.tpl.Execute(&buf, struct {
		Run            RunRef
		Task           TaskRef
		Instance       Instance
		CleanroomImage string
	}{in.Run, in.Task, in.Instance, cleanroom}); err != nil {
		return ComposeOutput{}, err
	}
	return ComposeOutput{
		Title:              fmt.Sprintf("[Benchmark] %s · %s", in.Run.DisplayName, in.Task.InstanceID),
		Description:        buf.String(),
		AssigneeAgentName:  "ProgramBenchRunner",
		SubmissionFilename: "submission.tar.gz",
	}, nil
}

// ----- SubmissionParser -----

type ProgramBenchParser struct{}

func NewProgramBenchParser() *ProgramBenchParser { return &ProgramBenchParser{} }

func (p *ProgramBenchParser) Kind() string { return programBenchKind }

func (p *ProgramBenchParser) Validate(ctx context.Context, att Attachment) error {
	if att.Filename != "submission.tar.gz" {
		return errors.New("submission filename must be exactly submission.tar.gz")
	}
	if att.SizeBytes > submissionMaxBytes {
		return fmt.Errorf("submission too large: %d bytes (max %d)", att.SizeBytes, int64(submissionMaxBytes))
	}
	return nil
}

func stringOf(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
