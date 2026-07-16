package jira

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// fieldmap.go — the custom-field type-compatibility matrix (FR-23) and value
// coercion between a Jira field's raw JSON and a Multica property value
// (FR-24). Unmappable values are reported so the caller skips that field
// without blocking the issue.

// PropertyType is a Multica issue-property type (issue_property.type CHECK).
type PropertyType string

const (
	PropText        PropertyType = "text"
	PropNumber      PropertyType = "number"
	PropSelect      PropertyType = "select"
	PropMultiSelect PropertyType = "multi_select"
	PropDate        PropertyType = "date"
	PropCheckbox    PropertyType = "checkbox"
	PropURL         PropertyType = "url"
)

// jiraTypeToProperty maps a Jira field schema type to the Multica property
// types it may bind to (FR-23 matrix). Multiple targets = editor choice.
var jiraTypeToProperty = map[string][]PropertyType{
	"string":   {PropText, PropURL},
	"number":   {PropNumber},
	"option":   {PropSelect},
	"priority": {PropSelect},
	"array":    {PropMultiSelect}, // options/labels-shaped
	"date":     {PropDate},
	"datetime": {PropDate},
	"boolean":  {PropCheckbox},
}

// TypesCompatible reports whether a Jira schema type may bind to a Multica
// property type (save-time validation).
func TypesCompatible(jiraType string, prop PropertyType) bool {
	for _, p := range jiraTypeToProperty[jiraType] {
		if p == prop {
			return true
		}
	}
	return false
}

// JiraRawToProperty coerces a Jira field's raw JSON into the Multica property
// value JSON (option/multi-select matched by name). ok=false means the value
// is unmappable — the caller skips that field with a journal entry (FR-24).
func JiraRawToProperty(raw json.RawMessage, prop PropertyType) (value json.RawMessage, ok bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	switch prop {
	case PropText, PropURL:
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return raw, true
		}
		return nil, false
	case PropNumber:
		var n json.Number
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.UseNumber()
		if d.Decode(&n) == nil {
			return raw, true
		}
		return nil, false
	case PropCheckbox:
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			return raw, true
		}
		return nil, false
	case PropDate:
		var s string
		if json.Unmarshal(raw, &s) == nil {
			// Jira dates are "2026-07-16" or full datetime; keep the date part.
			if len(s) >= 10 {
				out, _ := json.Marshal(s[:10])
				return out, true
			}
		}
		return nil, false
	case PropSelect:
		var opt struct {
			Value string `json:"value"`
			Name  string `json:"name"`
		}
		if json.Unmarshal(raw, &opt) == nil {
			name := opt.Value
			if name == "" {
				name = opt.Name
			}
			if name != "" {
				out, _ := json.Marshal(name)
				return out, true
			}
		}
		return nil, false
	case PropMultiSelect:
		var opts []struct {
			Value string `json:"value"`
			Name  string `json:"name"`
		}
		if json.Unmarshal(raw, &opts) == nil {
			var names []string
			for _, o := range opts {
				n := o.Value
				if n == "" {
					n = o.Name
				}
				if n != "" {
					names = append(names, n)
				}
			}
			out, _ := json.Marshal(names)
			return out, true
		}
		return nil, false
	}
	return nil, false
}

// PropertyToJiraRaw coerces a Multica property value into the Jira field's
// wire shape for an outbound PUT.
func PropertyToJiraRaw(value json.RawMessage, jiraType string) (any, bool) {
	if len(value) == 0 {
		return nil, false
	}
	switch jiraType {
	case "string":
		var s string
		if json.Unmarshal(value, &s) == nil {
			return s, true
		}
		return strings.Trim(string(value), `"`), true
	case "number":
		var n json.Number
		if json.Unmarshal(value, &n) == nil {
			if f, err := strconv.ParseFloat(n.String(), 64); err == nil {
				return f, true
			}
		}
		return nil, false
	case "boolean":
		var b bool
		if json.Unmarshal(value, &b) == nil {
			return b, true
		}
		return nil, false
	case "date", "datetime":
		var s string
		if json.Unmarshal(value, &s) == nil {
			return s, true
		}
		return nil, false
	case "option", "priority":
		var s string
		if json.Unmarshal(value, &s) == nil {
			return map[string]string{"value": s}, true
		}
		return nil, false
	case "array":
		var names []string
		if json.Unmarshal(value, &names) == nil {
			out := make([]map[string]string, 0, len(names))
			for _, n := range names {
				out = append(out, map[string]string{"value": n})
			}
			return out, true
		}
		return nil, false
	}
	return nil, false
}

// FieldMapError names both types on a matrix violation.
func FieldMapError(jiraType string, prop PropertyType) error {
	return fmt.Errorf("jira field type %q is not compatible with property type %q", jiraType, prop)
}
