package jira

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The planner is the PRD's semantic core; these tables pin modes × items ×
// divergence × leading exactly as §4.2/§4.4/§4.6 specify. Helpers below keep
// each case one screenful.

func baseItems(t *testing.T) ItemsV1 {
	t.Helper()
	it, err := ParseItems(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Seed snapshots: title/desc synced at "v0" on both sides.
	it.Title = ItemState{RemoteSHA: SHA("t0"), LocalSHA: SHA("t0")}
	it.Description = ItemState{RemoteSHA: SHA("d0"), LocalSHA: SHA("d0")}
	it.Status = StatusState{RemoteID: "100", Local: "todo"}
	it.Labels = LabelsState{LastSynced: []string{"bug"}, Propagated: []string{"bug"}}
	return it
}

func remote(title string, statusID string, labels ...string) *ObservedIssue {
	return &ObservedIssue{Key: "GAME-1", Summary: title, DescriptionMD: "d0", StatusID: statusID, StatusName: "S" + statusID, Labels: labels, Fields: map[string]string{}}
}

func local(title, status string, labels ...string) *LocalIssue {
	return &LocalIssue{Title: title, DescriptionMD: "d0", Status: status, Labels: labels, Fields: map[string]string{}}
}

func kinds(actions []Action) string {
	var parts []string
	for _, a := range actions {
		s := string(a.Kind)
		if a.Kind == ActSkip {
			s += ":" + string(a.Journal)
		}
		parts = append(parts, s+"@"+a.Item)
	}
	return strings.Join(parts, " ")
}

func settingsFor(mode string) Settings {
	s := DefaultSettings(mode)
	return s
}

var testStatusMap = StatusMap{
	In:  map[string]string{"100": "todo", "200": "in_progress", "300": "done"},
	Out: map[string]string{"todo": "100", "in_progress": "200", "done": "300"},
}

func plan(t *testing.T, mode string, leading string, r *ObservedIssue, l *LocalIssue) []Action {
	t.Helper()
	return planWithItems(t, mode, leading, r, l, baseItems(t))
}

func TestPlanScalarMatrix(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		leading string
		remote  *ObservedIssue
		local   *LocalIssue
		want    string // expected kinds@items in order (title item only, labels/status quiet)
	}{
		{"jira_leads remote change pulls", ModeJiraLeads, "", remote("t1", "100", "bug"), local("t0", "todo", "bug"), "in_title@title"},
		{"jira_leads local-only change survives silently", ModeJiraLeads, "", remote("t0", "100", "bug"), local("tX", "todo", "bug"), ""},
		{"jira_leads both changed: jira wins + breadcrumb", ModeJiraLeads, "", remote("t1", "100", "bug"), local("tX", "todo", "bug"), "breadcrumb_local@title in_title@title"},
		{"multica_leads local change pushes", ModeMulticaLeads, "", remote("t0", "100", "bug"), local("t1", "todo", "bug"), "out_title@title"},
		{"multica_leads remote-only change suppressed loudly", ModeMulticaLeads, "", remote("tX", "100", "bug"), local("t0", "todo", "bug"), "skip:inbound_suppressed@title"},
		{"multica_leads both changed: multica wins + remote breadcrumb", ModeMulticaLeads, "", remote("tX", "100", "bug"), local("t1", "todo", "bug"), "breadcrumb_remote@title out_title@title"},
		{"two_way leading jira both changed", ModeTwoWay, LeadJira, remote("t1", "100", "bug"), local("tX", "todo", "bug"), "breadcrumb_local@title in_title@title"},
		{"two_way leading multica both changed", ModeTwoWay, LeadMultica, remote("tX", "100", "bug"), local("t1", "todo", "bug"), "breadcrumb_remote@title out_title@title"},
		{"two_way remote only", ModeTwoWay, LeadJira, remote("t1", "100", "bug"), local("t0", "todo", "bug"), "in_title@title"},
		{"two_way local only", ModeTwoWay, LeadJira, remote("t0", "100", "bug"), local("t1", "todo", "bug"), "out_title@title"},
		{"mirror pulls and never pushes", ModeMirror, "", remote("t1", "100", "bug"), local("tX", "todo", "bug"), "breadcrumb_local@title in_title@title"},
		{"quiescent pair plans nothing", ModeTwoWay, LeadJira, remote("t0", "100", "bug"), local("t0", "todo", "bug"), ""},
	}
	for _, tc := range cases {
		got := kinds(plan(t, tc.mode, tc.leading, tc.remote, tc.local))
		if got != tc.want {
			t.Errorf("%s:\n got  %q\n want %q", tc.name, got, tc.want)
		}
	}
}

func TestPlanTitleAndDescriptionAreIndependentItems(t *testing.T) {
	r := remote("t1", "100", "bug") // title changed remotely
	l := local("t0", "todo", "bug")
	l.DescriptionMD = "dX" // description changed locally
	got := kinds(plan(t, ModeTwoWay, LeadJira, r, l))
	want := "in_title@title out_description@description"
	if got != want {
		t.Fatalf("cross-item edits must both survive (FR-8 per-item):\n got %q want %q", got, want)
	}
}

// planWithItems variant used by tests needing custom item state.
func planWithItems(t *testing.T, mode, leading string, r *ObservedIssue, l *LocalIssue, items ItemsV1) []Action {
	t.Helper()
	s := settingsFor(mode)
	if leading != "" {
		s.LeadingSystem = leading
	}
	return PlanIssue(PlanInput{Settings: s, StatusMap: testStatusMap, Items: items, Remote: r, Local: l})
}

func TestPlanBreadcrumbDedup(t *testing.T) {
	items := baseItems(t)
	items.Title.BreadcrumbFor = SHA("tX")
	got := kinds(planWithItems(t, ModeJiraLeads, "", remote("t1", "100", "bug"), local("tX", "todo", "bug"), items))
	if got != "in_title@title" {
		t.Fatalf("breadcrumb for the same discarded value must not repeat: %q", got)
	}
}

func TestPlanStatusSemantics(t *testing.T) {
	t.Run("inbound mapped", func(t *testing.T) {
		acts := plan(t, ModeJiraLeads, "", remote("t0", "200", "bug"), local("t0", "todo", "bug"))
		if len(acts) != 1 || acts[0].Kind != ActInStatus || acts[0].Target != "in_progress" {
			t.Fatalf("want in_status→in_progress, got %+v", acts)
		}
	})
	t.Run("inbound unmapped skips loudly", func(t *testing.T) {
		acts := plan(t, ModeJiraLeads, "", remote("t0", "999", "bug"), local("t0", "todo", "bug"))
		if len(acts) != 1 || acts[0].Kind != ActSkip || acts[0].Journal != JournalStatusUnmapped {
			t.Fatalf("want status_unmapped skip, got %+v", acts)
		}
	})
	t.Run("unchanged remote status never re-forces (change-driven, FR-16)", func(t *testing.T) {
		// Local status differs from mapping of remote, but remote did NOT
		// change → under jira_leads status is two_way: local change pushes.
		items := baseItems(t)
		items.Status = StatusState{RemoteID: "100", Local: "todo"}
		acts := planWithItems(t, ModeJiraLeads, "", remote("t0", "100", "bug"), local("t0", "in_progress", "bug"), items)
		if len(acts) != 1 || acts[0].Kind != ActOutTransition || acts[0].Target != "200" {
			t.Fatalf("local status change must push a transition, got %+v", acts)
		}
	})
	t.Run("mapped-equivalence kills N:1 flapping (FR-17)", func(t *testing.T) {
		// Jira status 200 maps to in_progress; local is in_progress → even
		// though Out[in_progress]=200 differs from... equals current: no-op.
		items := baseItems(t)
		items.Status = StatusState{RemoteID: "200", Local: "todo"}
		acts := planWithItems(t, ModeJiraLeads, "", nil, local("t0", "in_progress", "bug"), items)
		if len(acts) != 0 {
			t.Fatalf("mapped-equivalent statuses must not transition, got %+v", acts)
		}
	})
	t.Run("prospective mapping change alone plans nothing (FR-15)", func(t *testing.T) {
		s := settingsFor(ModeJiraLeads)
		otherMap := StatusMap{In: map[string]string{"100": "blocked"}, Out: map[string]string{"blocked": "100"}}
		acts := PlanIssue(PlanInput{Settings: s, StatusMap: otherMap, Items: baseItems(t),
			Remote: remote("t0", "100", "bug"), Local: local("t0", "todo", "bug")})
		if len(acts) != 0 {
			t.Fatalf("mapping edits are prospective; raw values unchanged ⇒ no actions, got %+v", acts)
		}
	})
}

func TestPlanLabelsOwnership(t *testing.T) {
	t.Run("pull adds and removes only propagated", func(t *testing.T) {
		items := baseItems(t)
		items.Labels = LabelsState{LastSynced: []string{"bug", "native"}, Propagated: []string{"bug"}}
		// Remote dropped both; native local label must survive (FR-22).
		acts := planWithItems(t, ModeJiraLeads, "", remote("t0", "100"), local("t0", "todo", "bug", "native"), items)
		if len(acts) != 1 || acts[0].Kind != ActInLabels {
			t.Fatalf("want one in_labels action, got %+v", acts)
		}
		if len(acts[0].Add) != 0 || strings.Join(acts[0].Remove, ",") != "bug" {
			t.Fatalf("only propagated labels may be removed: %+v", acts[0])
		}
	})
	t.Run("prefix filter scopes both directions", func(t *testing.T) {
		s := settingsFor(ModeJiraLeads)
		s.LabelPrefix = "sync-"
		items := baseItems(t)
		items.Labels = LabelsState{}
		acts := PlanIssue(PlanInput{Settings: s, StatusMap: testStatusMap, Items: items,
			Remote: remote("t0", "100", "noise", "sync-a"), Local: local("t0", "todo")})
		if len(acts) != 1 || acts[0].Kind != ActInLabels || strings.Join(acts[0].Add, ",") != "sync-a" {
			t.Fatalf("prefix filter must scope the set: %+v", acts)
		}
	})
	t.Run("two_way both-changed is set-level LWW by leading", func(t *testing.T) {
		items := baseItems(t)
		items.Labels = LabelsState{LastSynced: []string{"bug"}, Propagated: []string{"bug"}}
		acts := planWithItems(t, ModeTwoWay, LeadMultica,
			remote("t0", "100", "bug", "from-jira"),
			local("t0", "todo", "bug", "from-multica"), items)
		if len(acts) != 1 || acts[0].Kind != ActOutLabels || strings.Join(acts[0].Add, ",") != "from-multica" {
			t.Fatalf("leading=multica must push its set: %+v", acts)
		}
	})
}

func TestPlanFieldsRespectRowDirectionAndCarryMapping(t *testing.T) {
	s := settingsFor(ModeJiraLeads) // facet base = pull
	fieldMap := []FieldMapRow{
		{ExternalField: "customfield_1", PropertyID: "prop-1", Direction: "inherit"},
		{ExternalField: "customfield_2", PropertyID: "prop-2", Direction: "push"},
	}
	items := baseItems(t)
	items.Fields["customfield_1"] = ItemState{RemoteSHA: SHA(`"a"`), LocalSHA: SHA(`"a"`)}
	items.Fields["customfield_2"] = ItemState{RemoteSHA: SHA(`"b"`), LocalSHA: SHA(`"b"`)}

	r := remote("t0", "100", "bug")
	r.Fields = map[string]string{"customfield_1": `"a2"`, "customfield_2": `"b"`}
	l := local("t0", "todo", "bug")
	l.Fields = map[string]string{"prop-1": `"a"`, "prop-2": `"b2"`}

	acts := PlanIssue(PlanInput{Settings: s, StatusMap: testStatusMap, FieldMap: fieldMap, Items: items, Remote: r, Local: l})
	got := kinds(acts)
	want := "in_field@field:customfield_1 out_field@field:customfield_2"
	if got != want {
		t.Fatalf("field directions wrong:\n got %q\n want %q", got, want)
	}
	for _, a := range acts {
		if a.Detail["property_id"] == "" || a.Detail["external_field"] == "" {
			t.Fatalf("field action must carry mapping context: %+v", a)
		}
	}
}

func TestParseItemsVersioning(t *testing.T) {
	it, err := ParseItems([]byte(`{}`))
	if err != nil || it.V != 1 {
		t.Fatalf("empty init: %v %+v", err, it)
	}
	if _, err := ParseItems([]byte(`{"v":2}`)); err == nil {
		t.Fatal("newer items version must be refused (dirty-not-guess)")
	}
}

// TestDiffFileIsPure pins AD-11: the planner file imports stdlib only, so it
// can never grow I/O.
func TestDiffFileIsPure(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "diff.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(path, ".") {
			t.Errorf("diff.go must import stdlib only, found %q", path)
		}
	}
}
