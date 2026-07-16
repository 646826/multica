package jira

import (
	"encoding/json"
	"testing"
)

func TestTypesCompatibleMatrix(t *testing.T) {
	cases := []struct {
		jira string
		prop PropertyType
		want bool
	}{
		{"string", PropText, true},
		{"string", PropURL, true},
		{"string", PropNumber, false},
		{"number", PropNumber, true},
		{"option", PropSelect, true},
		{"priority", PropSelect, true},
		{"array", PropMultiSelect, true},
		{"array", PropSelect, false},
		{"date", PropDate, true},
		{"datetime", PropDate, true},
		{"boolean", PropCheckbox, true},
		{"boolean", PropText, false},
	}
	for _, c := range cases {
		if got := TypesCompatible(c.jira, c.prop); got != c.want {
			t.Errorf("TypesCompatible(%q,%q)=%v want %v", c.jira, c.prop, got, c.want)
		}
	}
}

func TestJiraRawToPropertyCoercions(t *testing.T) {
	v, ok := JiraRawToProperty(json.RawMessage(`{"value":"High"}`), PropSelect)
	if !ok || string(v) != `"High"` {
		t.Fatalf("select by value: %s %v", v, ok)
	}
	v, ok = JiraRawToProperty(json.RawMessage(`[{"value":"a"},{"value":"b"}]`), PropMultiSelect)
	if !ok || string(v) != `["a","b"]` {
		t.Fatalf("multi-select: %s %v", v, ok)
	}
	v, ok = JiraRawToProperty(json.RawMessage(`"2026-07-16T10:00:00.000+0000"`), PropDate)
	if !ok || string(v) != `"2026-07-16"` {
		t.Fatalf("date truncation: %s %v", v, ok)
	}
	if _, ok := JiraRawToProperty(json.RawMessage(`{"value":"x"}`), PropNumber); ok {
		t.Fatal("option into number must be unmappable")
	}
}

func TestPropertyToJiraRaw(t *testing.T) {
	if v, ok := PropertyToJiraRaw(json.RawMessage(`"High"`), "option"); !ok || v.(map[string]string)["value"] != "High" {
		t.Fatalf("option out: %+v %v", v, ok)
	}
	if v, ok := PropertyToJiraRaw(json.RawMessage(`["a","b"]`), "array"); !ok {
		t.Fatalf("array out: %+v %v", v, ok)
	}
	if v, ok := PropertyToJiraRaw(json.RawMessage(`42`), "number"); !ok || v.(float64) != 42 {
		t.Fatalf("number out: %+v %v", v, ok)
	}
}
