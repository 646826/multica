package jira

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// diff.go is the pure planner (AD-5, AD-11): given the Connection's settings,
// a Link's two-sided last-synced item state, and what was observed on each
// side, it emits the minimal apply actions. No I/O, no clocks — every PRD
// semantic (modes, Leading system, divergence Breadcrumbs, change-driven
// status, mapped-equivalence, label ownership, prospective mappings) is
// decided here and pinned by table tests.

// Facet identifiers (PRD Glossary).
type Facet string

const (
	FacetFields       Facet = "fields"
	FacetStatus       Facet = "status"
	FacetComments     Facet = "comments"
	FacetLabels       Facet = "labels"
	FacetCustomFields Facet = "custom_fields"
)

// Direction is a facet's effective flow.
type Direction int

const (
	DirOff Direction = iota
	DirPull
	DirPush
	DirTwoWay
)

// EffectiveDirection resolves mode + facet toggles into a flow direction
// (PRD §3 "Sync mode" + FR-7). fields and status are always in scope.
func EffectiveDirection(s Settings, f Facet) Direction {
	switch f {
	case FacetComments:
		if !s.CommentsEnabled {
			return DirOff
		}
	case FacetLabels:
		if !s.LabelsEnabled {
			return DirOff
		}
	case FacetCustomFields:
		if !s.CustomFieldsEnabled {
			return DirOff
		}
	}
	switch s.Mode {
	case ModeMirror:
		return DirPull
	case ModeJiraLeads:
		// Pull everything in scope; push only comments and status.
		switch f {
		case FacetComments, FacetStatus:
			return DirTwoWay
		default:
			return DirPull
		}
	case ModeMulticaLeads:
		// Push everything in scope; pull comments.
		if f == FacetComments {
			return DirTwoWay
		}
		return DirPush
	case ModeTwoWay:
		return DirTwoWay
	}
	return DirOff
}

// EffectiveLeading resolves the conflict winner (PRD Glossary "Leading system").
func EffectiveLeading(s Settings) string {
	switch s.Mode {
	case ModeJiraLeads, ModeMirror:
		return LeadJira
	case ModeMulticaLeads:
		return LeadMultica
	default:
		return s.LeadingSystem
	}
}

// --- Persisted per-item state (pinned jira_link.items v1 shape, AD-5) ---

type ItemState struct {
	RemoteSHA     string `json:"remote_sha,omitempty"`
	LocalSHA      string `json:"local_sha,omitempty"`
	BreadcrumbFor string `json:"breadcrumb_for,omitempty"`
}

type StatusState struct {
	RemoteID string `json:"remote_id,omitempty"`
	// RemoteCategory is the Jira status category (new|indeterminate|done) of the
	// last-synced remote status. Persisted so the terminal-stop guard works on
	// the local-only outbound path, where the remote is not observed this cycle.
	RemoteCategory string `json:"remote_category,omitempty"`
	Local          string `json:"local,omitempty"`
	BreadcrumbFor  string `json:"breadcrumb_for,omitempty"`
	// UnreachableFor suppresses re-journaling the same doomed transition
	// every cycle: SHA(local status + target id) of the last loud failure,
	// cleared when the local status changes or the transition succeeds.
	UnreachableFor string `json:"unreachable_for,omitempty"`
}

type LabelsState struct {
	LastSynced []string `json:"last_synced,omitempty"`
	Propagated []string `json:"propagated,omitempty"`
}

type ItemsV1 struct {
	V           int                  `json:"v"`
	Title       ItemState            `json:"title"`
	Description ItemState            `json:"description"`
	Status      StatusState          `json:"status"`
	Labels      LabelsState          `json:"labels"`
	Fields      map[string]ItemState `json:"fields,omitempty"`
	Tag         TagState             `json:"tag"`
}

// TagState tracks edge-triggered agent tagging (FR-27): which Jira signals
// have already fired a rule, and which agent sync last assigned (the
// human-precedence guard compares the live assignee against it).
type TagState struct {
	FiredLabels   []string `json:"fired_labels,omitempty"`
	FiredAssignee string   `json:"fired_assignee,omitempty"`
	AssignedAgent string   `json:"assigned_agent,omitempty"`
}

// ParseItems decodes the persisted item state. An empty document initializes
// v1; a NEWER version than this build understands is refused (the caller
// marks the Link dirty rather than guessing at unknown semantics).
func ParseItems(raw []byte) (ItemsV1, error) {
	var it ItemsV1
	if len(raw) == 0 || string(raw) == "{}" {
		return ItemsV1{V: 1, Fields: map[string]ItemState{}}, nil
	}
	if err := json.Unmarshal(raw, &it); err != nil {
		return it, fmt.Errorf("jira_link.items: %w", err)
	}
	if it.V == 0 {
		it.V = 1
	}
	if it.V > 1 {
		return it, fmt.Errorf("jira_link.items: unknown version %d", it.V)
	}
	if it.Fields == nil {
		it.Fields = map[string]ItemState{}
	}
	return it, nil
}

// SHA is the snapshot hash for scalar items: sha256 hex over the exact
// side-local representation (raw remote value / canonical local value).
func SHA(v string) string {
	if v == "" {
		return ""
	}
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// --- Plan input/output ---

// LocalIssue is the Multica-side observation of a Linked issue.
type LocalIssue struct {
	Title         string
	DescriptionMD string
	Status        string
	Labels        []string
	Fields        map[string]string // property id → canonical value (JSON text)
}

// PlanInput bundles everything the planner needs for one Linked pair.
// Remote is nil when the Jira side produced no observation this cycle;
// Local carries the CURRENT Multica values (always available — reading local
// state is free; change detection still runs against snapshots).
type PlanInput struct {
	Settings  Settings
	StatusMap StatusMap
	FieldMap  []FieldMapRow
	Items     ItemsV1
	Remote    *ObservedIssue
	Local     *LocalIssue
}

// ActionKind enumerates planner outputs.
type ActionKind string

const (
	ActInTitle       ActionKind = "in_title"
	ActInDescription ActionKind = "in_description"
	ActInStatus      ActionKind = "in_status"
	ActInLabels      ActionKind = "in_labels"
	ActInField       ActionKind = "in_field"
	ActOutTitle      ActionKind = "out_title"
	ActOutDesc       ActionKind = "out_description"
	ActOutTransition ActionKind = "out_transition"
	ActOutLabels     ActionKind = "out_labels"
	ActOutField      ActionKind = "out_field"
	ActBreadcrumbIn  ActionKind = "breadcrumb_local"  // posted on the Multica side
	ActBreadcrumbOut ActionKind = "breadcrumb_remote" // posted on the Jira side
	ActSkip          ActionKind = "skip"
)

// Action is one planned apply step. Item names the per-item scope
// ("title" | "description" | "status" | "labels" | "field:<id>").
type Action struct {
	Kind    ActionKind
	Item    string
	Value   string   // new value for scalar applies (canonical/raw per side)
	Add     []string // label additions (target side)
	Remove  []string // label removals (target side; ⊆ propagated on receiver)
	Target  string   // in_status: multica status · out_transition: jira status id
	Old     string   // breadcrumb: discarded value (bounded by applier)
	New     string   // breadcrumb: winning value
	Journal JournalKind
	Detail  map[string]any
}

// PlanIssue emits the actions for one Linked pair. Deterministic order:
// title, description, status, labels, fields (map iterated in sorted order).
func PlanIssue(in PlanInput) []Action {
	var out []Action
	dirFields := EffectiveDirection(in.Settings, FacetFields)
	leading := EffectiveLeading(in.Settings)

	// title + description (per-item within the fields facet, FR-8)
	if in.Remote != nil || in.Local != nil {
		out = append(out, planScalar(scalarPlan{
			item: "title", dir: dirFields, leading: leading,
			state:     in.Items.Title,
			remoteVal: remoteStr(in.Remote, func(r *ObservedIssue) string { return r.Summary }),
			remoteSet: in.Remote != nil,
			localVal:  localStr(in.Local, func(l *LocalIssue) string { return l.Title }),
			localSet:  in.Local != nil,
			inKind:    ActInTitle, outKind: ActOutTitle,
		})...)
		out = append(out, planScalar(scalarPlan{
			item: "description", dir: dirFields, leading: leading,
			state:     in.Items.Description,
			remoteVal: remoteStr(in.Remote, func(r *ObservedIssue) string { return r.DescriptionMD }),
			remoteSet: in.Remote != nil,
			localVal:  localStr(in.Local, func(l *LocalIssue) string { return l.DescriptionMD }),
			localSet:  in.Local != nil,
			inKind:    ActInDescription, outKind: ActOutDesc,
		})...)
	}

	out = append(out, planStatus(in, leading)...)
	out = append(out, planLabels(in, leading)...)
	out = append(out, planFields(in, leading)...)
	return out
}

type scalarPlan struct {
	item      string
	dir       Direction
	leading   string
	state     ItemState
	remoteVal string
	remoteSet bool // remote side observed this cycle
	localVal  string
	localSet  bool
	inKind    ActionKind
	outKind   ActionKind
}

func planScalar(p scalarPlan) []Action {
	if p.dir == DirOff {
		return nil
	}
	remoteChanged := p.remoteSet && SHA(p.remoteVal) != p.state.RemoteSHA
	localChanged := p.localSet && SHA(p.localVal) != p.state.LocalSHA

	var out []Action
	breadcrumbLocal := func(oldV, newV string) {
		if p.state.BreadcrumbFor == SHA(oldV) {
			return // already posted for this exact discarded value (FR-8)
		}
		out = append(out, Action{Kind: ActBreadcrumbIn, Item: p.item, Old: oldV, New: newV})
	}
	breadcrumbRemote := func(oldV, newV string) {
		if p.state.BreadcrumbFor == SHA(oldV) {
			return
		}
		out = append(out, Action{Kind: ActBreadcrumbOut, Item: p.item, Old: oldV, New: newV})
	}

	switch p.dir {
	case DirPull:
		if remoteChanged {
			if localChanged {
				breadcrumbLocal(p.localVal, p.remoteVal)
			}
			out = append(out, Action{Kind: p.inKind, Item: p.item, Value: p.remoteVal})
		} else if localChanged {
			// Local edit on the non-flowing side survives until the leading
			// side next changes this item (FR-8); nothing to do now.
		}
	case DirPush:
		if localChanged {
			if remoteChanged {
				breadcrumbRemote(p.remoteVal, p.localVal)
			}
			out = append(out, Action{Kind: p.outKind, Item: p.item, Value: p.localVal})
		} else if remoteChanged {
			out = append(out, Action{Kind: ActSkip, Item: p.item, Journal: JournalInboundSuppressed,
				Detail: map[string]any{"direction": "push"}})
		}
	case DirTwoWay:
		switch {
		case remoteChanged && localChanged:
			if p.leading == LeadJira {
				breadcrumbLocal(p.localVal, p.remoteVal)
				out = append(out, Action{Kind: p.inKind, Item: p.item, Value: p.remoteVal})
			} else {
				breadcrumbRemote(p.remoteVal, p.localVal)
				out = append(out, Action{Kind: p.outKind, Item: p.item, Value: p.localVal})
			}
		case remoteChanged:
			// Receiver may hold an older un-pushed divergence (snapshot gap):
			// two_way applies the fresh remote change; divergence handling
			// above only triggers when both changed this cycle.
			out = append(out, Action{Kind: p.inKind, Item: p.item, Value: p.remoteVal})
		case localChanged:
			out = append(out, Action{Kind: p.outKind, Item: p.item, Value: p.localVal})
		}
	}
	return out
}

func planStatus(in PlanInput, leading string) []Action {
	dir := EffectiveDirection(in.Settings, FacetStatus)
	if dir == DirOff {
		return nil
	}
	st := in.Items.Status
	remoteChanged := in.Remote != nil && in.Remote.StatusID != "" && in.Remote.StatusID != st.RemoteID
	localChanged := in.Local != nil && in.Local.Status != "" && in.Local.Status != st.Local

	// Current remote status (last-synced when unobserved this cycle).
	currentRemoteID := st.RemoteID
	if in.Remote != nil && in.Remote.StatusID != "" {
		currentRemoteID = in.Remote.StatusID
	}

	var out []Action
	planIn := func() {
		target, ok := in.StatusMap.In[in.Remote.StatusID]
		if !ok {
			out = append(out, Action{Kind: ActSkip, Item: "status", Journal: JournalStatusUnmapped,
				Detail: map[string]any{"jira_status_id": in.Remote.StatusID, "jira_status": in.Remote.StatusName}})
			return
		}
		out = append(out, Action{Kind: ActInStatus, Item: "status", Value: in.Remote.StatusID, Target: target})
	}
	planOut := func() {
		// Mapped-equivalence guard (FR-17): no transition when Jira's current
		// status already maps to the local status — kills N:1 flapping.
		if mapped, ok := in.StatusMap.In[currentRemoteID]; ok && mapped == in.Local.Status {
			return
		}
		// Terminal-stop safety (RU §12.2): the sides genuinely differ, but Jira
		// is in a Done category (a human moved it to Done/Cancelled). Never cross
		// that with a late automated transition — suppress and surface it. Reads
		// the SNAPSHOT category so it also fires on the local-only outbound path
		// (remote unobserved this cycle) — the exact path a human-cancel-then-
		// agent-complete race takes; the terminal snapshot was stored by the
		// earlier inbound pull.
		remoteCategory := in.Items.Status.RemoteCategory
		if in.Remote != nil {
			remoteCategory = in.Remote.StatusCategory
		}
		if remoteCategory == "done" {
			out = append(out, Action{Kind: ActSkip, Item: "status", Journal: JournalStatusTerminalGuard,
				Detail: map[string]any{"jira_status_id": currentRemoteID, "multica_status": in.Local.Status}})
			return
		}
		targetID, ok := in.StatusMap.Out[in.Local.Status]
		if !ok {
			out = append(out, Action{Kind: ActSkip, Item: "status", Journal: JournalStatusUnmapped,
				Detail: map[string]any{"multica_status": in.Local.Status, "direction": "out"}})
			return
		}
		if targetID == currentRemoteID {
			return
		}
		out = append(out, Action{Kind: ActOutTransition, Item: "status", Value: in.Local.Status, Target: targetID})
	}

	switch dir {
	case DirPull:
		if remoteChanged {
			planIn()
		}
	case DirPush:
		if localChanged {
			planOut()
		} else if remoteChanged {
			out = append(out, Action{Kind: ActSkip, Item: "status", Journal: JournalInboundSuppressed,
				Detail: map[string]any{"direction": "push"}})
		}
	case DirTwoWay:
		switch {
		case remoteChanged && localChanged:
			if leading == LeadJira {
				planIn()
			} else {
				planOut()
			}
		case remoteChanged:
			planIn()
		case localChanged:
			planOut()
		}
	}
	return out
}

func planLabels(in PlanInput, leading string) []Action {
	dir := EffectiveDirection(in.Settings, FacetLabels)
	if dir == DirOff || (in.Remote == nil && in.Local == nil) {
		return nil
	}
	prefix := in.Settings.LabelPrefix
	last := filterLabels(in.Items.Labels.LastSynced, prefix)
	propagated := toSet(in.Items.Labels.Propagated)

	var remote, local []string
	remoteSet, localSet := false, false
	if in.Remote != nil {
		remote = filterLabels(in.Remote.Labels, prefix)
		remoteSet = true
	}
	if in.Local != nil {
		local = filterLabels(in.Local.Labels, prefix)
		localSet = true
	}
	remoteChanged := remoteSet && !sameSet(remote, last)
	localChanged := localSet && !sameSet(local, last)

	var out []Action
	planIn := func() {
		add, remove := setDiff(remote, local)
		// Ownership protection (FR-22): only remove local labels sync itself
		// propagated; native local labels survive.
		remove = intersect(remove, propagated)
		if len(add)+len(remove) > 0 {
			out = append(out, Action{Kind: ActInLabels, Item: "labels", Add: add, Remove: remove})
		}
	}
	planOut := func() {
		add, remove := setDiff(local, remote)
		remove = intersect(remove, propagated)
		if len(add)+len(remove) > 0 {
			out = append(out, Action{Kind: ActOutLabels, Item: "labels", Add: add, Remove: remove})
		}
	}

	switch dir {
	case DirPull:
		if remoteChanged {
			planIn()
		}
	case DirPush:
		if localChanged {
			planOut()
		} else if remoteChanged {
			out = append(out, Action{Kind: ActSkip, Item: "labels", Journal: JournalInboundSuppressed,
				Detail: map[string]any{"direction": "push"}})
		}
	case DirTwoWay:
		switch {
		case remoteChanged && localChanged:
			// Set-level LWW under the Leading system (PRD non-goal: no
			// element merge).
			if leading == LeadJira {
				planIn()
			} else {
				planOut()
			}
		case remoteChanged:
			planIn()
		case localChanged:
			planOut()
		}
	}
	return out
}

func planFields(in PlanInput, leading string) []Action {
	baseDir := EffectiveDirection(in.Settings, FacetCustomFields)
	if len(in.FieldMap) == 0 {
		return nil
	}
	var out []Action
	rows := append([]FieldMapRow(nil), in.FieldMap...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ExternalField < rows[j].ExternalField })
	for _, row := range rows {
		dir := baseDir
		switch row.Direction {
		case "pull":
			dir = DirPull
		case "push":
			dir = DirPush
		case "two_way":
			dir = DirTwoWay
		}
		if baseDir == DirOff {
			dir = DirOff // facet toggle wins over per-row direction
		}
		item := "field:" + row.ExternalField
		state := in.Items.Fields[row.ExternalField]
		acts := planScalar(scalarPlan{
			item: item, dir: dir, leading: leading,
			state:     state,
			remoteVal: remoteField(in.Remote, row.ExternalField),
			remoteSet: in.Remote != nil,
			localVal:  localField(in.Local, row.PropertyID),
			localSet:  in.Local != nil,
			inKind:    ActInField, outKind: ActOutField,
		})
		// Field actions need the mapping row context for appliers.
		for i := range acts {
			if acts[i].Detail == nil {
				acts[i].Detail = map[string]any{}
			}
			acts[i].Detail["external_field"] = row.ExternalField
			acts[i].Detail["property_id"] = row.PropertyID
		}
		out = append(out, acts...)
	}
	return out
}

// --- helpers ---

func remoteStr(r *ObservedIssue, f func(*ObservedIssue) string) string {
	if r == nil {
		return ""
	}
	return f(r)
}

func localStr(l *LocalIssue, f func(*LocalIssue) string) string {
	if l == nil {
		return ""
	}
	return f(l)
}

func remoteField(r *ObservedIssue, id string) string {
	if r == nil {
		return ""
	}
	return r.Fields[id]
}

func localField(l *LocalIssue, propertyID string) string {
	if l == nil {
		return ""
	}
	return l.Fields[propertyID]
}

func filterLabels(labels []string, prefix string) []string {
	if prefix == "" {
		return normalizeSet(labels)
	}
	var out []string
	for _, l := range labels {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return normalizeSet(out)
}

func normalizeSet(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func toSet(in []string) map[string]bool {
	m := map[string]bool{}
	for _, v := range in {
		m[v] = true
	}
	return m
}

func sameSet(a, b []string) bool {
	a, b = normalizeSet(a), normalizeSet(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// intersect keeps only members present in the allowed set.
func intersect(values []string, allowed map[string]bool) []string {
	var out []string
	for _, v := range values {
		if allowed[v] {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// setDiff returns desired−current (add) and current−desired (remove).
func setDiff(desired, current []string) (add, remove []string) {
	d, c := toSet(desired), toSet(current)
	for v := range d {
		if !c[v] {
			add = append(add, v)
		}
	}
	for v := range c {
		if !d[v] {
			remove = append(remove, v)
		}
	}
	sort.Strings(add)
	sort.Strings(remove)
	return add, remove
}
