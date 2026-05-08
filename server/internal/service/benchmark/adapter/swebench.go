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
	sweBenchKind         = "swe_bench"
	sweBenchMaxSubmBytes = 5 * 1024 * 1024 // 5 MiB
)

// ----- Catalog -----

// SWEBenchCatalog resolves SWE-bench instance ids to typed metadata. Like
// ProgramBenchCatalog it shells out to a uvx-managed Python helper; SWE-bench
// is also pip-installable, so the same uvx pattern works.
type SWEBenchCatalog struct {
	mu      sync.Mutex
	cache   map[string]Instance
	runArgs func(ctx context.Context, args ...string) ([]byte, error)
}

func NewSWEBenchCatalog() *SWEBenchCatalog {
	return &SWEBenchCatalog{
		cache: map[string]Instance{},
		runArgs: func(ctx context.Context, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(cctx, args[0], args[1:]...)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Run(); err != nil {
				return out.Bytes(), fmt.Errorf("uvx swebench: %w (output=%s)", err, strings.TrimSpace(out.String()))
			}
			return out.Bytes(), nil
		},
	}
}

func (c *SWEBenchCatalog) Kind() string { return sweBenchKind }

func (c *SWEBenchCatalog) Resolve(ctx context.Context, instanceID string) (Instance, error) {
	c.mu.Lock()
	if cached, ok := c.cache[instanceID]; ok {
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	raw, err := c.runArgs(ctx, "uvx", "--from", "swebench", "python", "-c",
		fmt.Sprintf(`import json; from swebench import data; print(json.dumps(data.task(%q)))`, instanceID))
	if err != nil {
		return Instance{}, fmt.Errorf("swebench resolve %s: %w", instanceID, err)
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

// List is intentionally unimplemented in v1: SWE-bench has thousands of
// instances and indiscriminate listing is rarely useful here. Operators
// curate suites by passing instance ids manually.
func (c *SWEBenchCatalog) List(ctx context.Context, filter ListFilter) ([]Instance, error) {
	return nil, errors.New("swe_bench: List not implemented in v1; provide instance ids manually")
}

// ----- IssueComposer -----

type SWEBenchComposer struct {
	tpl *template.Template
}

const sweBenchTemplate = "# {{.Run.DisplayName}} — {{.Task.InstanceID}}\n\n" +
	"**Instance:** `{{.Task.InstanceID}}`\n" +
	"**Language:** {{.Instance.Language}}    **Difficulty:** {{.Instance.Difficulty}}\n\n" +
	"You are the **SWE-bench solver** agent. Solve the bug described in this issue and attach a unified diff `solution.patch` to a comment on this issue.\n\n" +
	"## Submission contract\n" +
	"Attach a single artifact named exactly `solution.patch`. The diff should apply with `git apply` from the repository root.\n\n" +
	"Run SWE-bench's standard evaluator locally to verify before submitting:\n" +
	"```bash\nuvx --from swebench python -m swebench.harness --instance {{.Task.InstanceID}} --patch ./solution.patch\n```\n\n" +
	"## Rules\n" +
	"- Do not search the web for the canonical fix.\n" +
	"- Do not browse the project's issue tracker or PR history.\n" +
	"- Use only the test cases provided in the issue's `test_patch` block to validate.\n"

func NewSWEBenchComposer() *SWEBenchComposer {
	return &SWEBenchComposer{
		tpl: template.Must(template.New("swebench").Parse(sweBenchTemplate)),
	}
}

func (c *SWEBenchComposer) Kind() string { return sweBenchKind }

func (c *SWEBenchComposer) Compose(ctx context.Context, in ComposeInput) (ComposeOutput, error) {
	var buf bytes.Buffer
	if err := c.tpl.Execute(&buf, struct {
		Run      RunRef
		Task     TaskRef
		Instance Instance
	}{in.Run, in.Task, in.Instance}); err != nil {
		return ComposeOutput{}, err
	}
	return ComposeOutput{
		Title:              fmt.Sprintf("[SWE-bench] %s · %s", in.Run.DisplayName, in.Task.InstanceID),
		Description:        buf.String(),
		AssigneeAgentName:  "SWEBenchSolver",
		SubmissionFilename: "solution.patch",
	}, nil
}

// ----- SubmissionParser -----

type SWEBenchParser struct{}

func NewSWEBenchParser() *SWEBenchParser { return &SWEBenchParser{} }

func (p *SWEBenchParser) Kind() string { return sweBenchKind }

func (p *SWEBenchParser) Validate(ctx context.Context, att Attachment) error {
	if att.Filename != "solution.patch" {
		return errors.New("submission filename must be exactly solution.patch")
	}
	if att.SizeBytes > sweBenchMaxSubmBytes {
		return fmt.Errorf("submission too large: %d bytes (max %d)", att.SizeBytes, int64(sweBenchMaxSubmBytes))
	}
	return nil
}
