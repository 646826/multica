# Multica × ProgramBench Phase 7 — Auto-fetch Reference Patches Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Replace the manual "paste reference patch" step in the Replay-suite-create UI with a "Fetch from URL" button. Operator pastes a PR URL (Azure DevOps, GitHub, or any provider that exposes `.diff` / `.patch`) and the backend pulls the diff text.

**Approach:** Generic URL fetcher with provider-detection. No per-provider auth schemes in v1 — use either env-configured PAT (for ADO) or just plain HTTPS (for public GitHub). Workspace-scoped via existing handler middleware.

---

## Tasks

### T01 — Reference fetcher service

**Files:**
- `server/internal/service/benchmark/reference_fetcher.go`
- `server/internal/service/benchmark/reference_fetcher_test.go`

```go
type ReferenceFetcher struct {
    httpClient *http.Client
    adoToken   string  // from MULTICA_ADO_PAT env
    githubToken string // from MULTICA_GITHUB_TOKEN env
}

func NewReferenceFetcher(adoToken, githubToken string) *ReferenceFetcher

// FetchPatchFromURL:
// - github.com/<owner>/<repo>/pull/<n>     → fetch <url>.diff with optional GitHub token
// - dev.azure.com/<org>/<project>/_git/<repo>/pullrequest/<n> →
//     POST GET to /pullRequests/<n>/diffs?api-version=7.0 with Basic auth ":<pat>"
// - any URL ending .patch or .diff      → plain HTTPS GET
// Returns patch text + canonical source URL.
```

Tests: stub `httpClient.Transport` with `httptest.RoundTripFunc` style for each provider; verify auth headers + URL transforms.

Commit: `feat(benchmark): reference patch fetcher for replay suites`.

### T02 — HTTP endpoint

**Files:**
- `server/internal/handler/benchmark.go` — add `POST /api/benchmarks/replay/fetch-reference`

```go
// Body: {url: string}
// Returns: {patch: string, source_url: string} | 502 fetch_failed | 400 unsupported_url
func (h *BenchmarkHandler) FetchReplayReference(w http.ResponseWriter, r *http.Request)
```

Wire into router under existing `/api/benchmarks/replay/` group.

Add `ReferenceFetcher *benchmark.ReferenceFetcher` to BenchmarkDeps; build in router.go from env vars `MULTICA_ADO_PAT` and `MULTICA_GITHUB_TOKEN`.

Commit: `feat(benchmark): /api/benchmarks/replay/fetch-reference endpoint`.

### T03 — Frontend hook + UI button

**Files:**
- `packages/core/types/benchmark.ts` — add `FetchReferenceRequest`/`FetchReferenceResponse`
- `packages/core/api/client.ts` — `fetchReplayReference(url)`
- `packages/core/benchmarks/mutations.ts` — `useFetchReplayReference()`
- `packages/views/benchmarks/SuiteCreate.tsx` — add button next to each ReferencePatchEditor

UI flow:
1. Input: PR URL textbox + "Fetch" button.
2. On click: call mutation; on success populate the patch textarea below; also set `reference_pr_url`.
3. On error: inline alert below the URL input.

Locale keys: `suite_create.replay_fetch_url_label`, `replay_fetch_button`, `replay_fetch_error_*`.

Commit: `feat(benchmark): UI button to fetch reference patch from PR URL`.

### T04 — Final check + push

Tests, lint, typecheck. Push to fork.

Commit: `(no new — just push)`.

---

## Self-Review

**Scope intentionally minimal:**
- ✅ ADO + GitHub coverage with two env-configured tokens (no DB schema for credentials).
- ✅ Plain HTTPS for `.patch`/`.diff` URLs (catches GitLab, Bitbucket, raw paths).
- ⏭ Per-workspace integration credentials in DB — defer; would need encrypted-at-rest pattern.
- ⏭ Jira issue auto-population (title/description from JIRA-XXX key) — defer; the existing replay flow uses Multica issues directly which already mirror Jira via the Multica/Jira pump.
- ⏭ Caching fetched patches — defer.

If the operator hasn't set env tokens, ADO/private-repo fetches return 502 with a helpful message; public GitHub still works without a token (rate-limited).
