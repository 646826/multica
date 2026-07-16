// Package jira implements the native Jira Cloud sync integration:
// a per-Connection state-based reconcile loop that mirrors issues, statuses,
// comments, labels and mapped custom fields between one Multica workspace and
// one Jira project, with configurable sync modes and a designated leading
// system.
//
// Design contract: _bmad/output/architecture/architecture-multica-jira-sync-2026-07-16/ARCHITECTURE-SPINE.md
// (AD-1..AD-15) over PRD _bmad/output/prds/prd-multica-jira-sync-2026-07-16/prd.md.
// The package is flat by convention (client, config, reconcile, diff, apply,
// convert, mention, rules, journal, worker); nothing outside cmd/server and
// internal/handler may import it.
package jira
