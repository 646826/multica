package jira

import (
	"encoding/json"
	"fmt"
	"strings"
)

// convert.go is the ONLY rich-text boundary (AD-7): the ADF reader below and
// the Markdown canonicalizer both live here so every snapshot (AD-5) is built
// from one deterministic representation. Supported profile: paragraphs,
// headings, bold/italic/code, ordered/unordered lists, code blocks, links,
// plain mentions. Anything else degrades to readable text; the caller
// journals the degradation once per issue-item.

// adfNode is the generic ADF tree shape.
type adfNode struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	Content []adfNode       `json:"content,omitempty"`
	Attrs   map[string]any  `json:"attrs,omitempty"`
	Marks   []adfMark       `json:"marks,omitempty"`
	Version json.RawMessage `json:"version,omitempty"`
}

type adfMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// ADFToMarkdown renders an ADF document (raw JSON) into canonical Markdown.
// It never fails on unknown input: unknown nodes contribute their readable
// text plus a placeholder, and `lossy` reports that degradation happened.
func ADFToMarkdown(raw []byte) (md string, lossy bool) {
	if len(raw) == 0 {
		return "", false
	}
	var doc adfNode
	if err := json.Unmarshal(raw, &doc); err != nil {
		// Not ADF (plain text or garbage): pass through as text.
		return CanonicalMarkdown(string(raw)), true
	}
	var b strings.Builder
	l := renderBlocks(&b, doc.Content, "")
	return CanonicalMarkdown(b.String()), l
}

func renderBlocks(b *strings.Builder, nodes []adfNode, indent string) (lossy bool) {
	for _, n := range nodes {
		switch n.Type {
		case "paragraph":
			b.WriteString(indent)
			lossy = renderInline(b, n.Content) || lossy
			b.WriteString("\n\n")
		case "heading":
			level := 1
			if v, ok := n.Attrs["level"].(float64); ok && v >= 1 && v <= 6 {
				level = int(v)
			}
			b.WriteString(strings.Repeat("#", level) + " ")
			lossy = renderInline(b, n.Content) || lossy
			b.WriteString("\n\n")
		case "bulletList":
			for _, item := range n.Content {
				b.WriteString(indent + "- ")
				lossy = renderListItem(b, item, indent) || lossy
			}
			b.WriteString("\n")
		case "orderedList":
			for i, item := range n.Content {
				fmt.Fprintf(b, "%s%d. ", indent, i+1)
				lossy = renderListItem(b, item, indent) || lossy
			}
			b.WriteString("\n")
		case "codeBlock":
			lang := ""
			if v, ok := n.Attrs["language"].(string); ok {
				lang = v
			}
			b.WriteString("```" + lang + "\n")
			for _, c := range n.Content {
				b.WriteString(c.Text)
			}
			b.WriteString("\n```\n\n")
		case "blockquote":
			var inner strings.Builder
			lossy = renderBlocks(&inner, n.Content, "") || lossy
			for _, line := range strings.Split(strings.TrimRight(inner.String(), "\n"), "\n") {
				b.WriteString("> " + line + "\n")
			}
			b.WriteString("\n")
		case "rule":
			b.WriteString("---\n\n")
		case "mediaGroup", "mediaSingle", "media":
			// Attachments do not transfer in v1 (PRD non-goal); degrade
			// readably instead of dropping silently (FR-12).
			b.WriteString(indent + "[attachment]\n\n")
			lossy = true
		case "table":
			b.WriteString(indent + "[table]\n\n")
			collectText(b, n.Content)
			b.WriteString("\n")
			lossy = true
		default:
			// Unknown block: keep its readable text, flag lossiness.
			if len(n.Content) > 0 || n.Text != "" {
				collectText(b, []adfNode{n})
				b.WriteString("\n\n")
			}
			lossy = true
		}
	}
	return lossy
}

func renderListItem(b *strings.Builder, item adfNode, indent string) (lossy bool) {
	// listItem wraps block nodes; render its first paragraph inline and any
	// remaining blocks indented beneath the bullet.
	first := true
	for _, c := range item.Content {
		if c.Type == "paragraph" && first {
			lossy = renderInline(b, c.Content) || lossy
			b.WriteString("\n")
			first = false
			continue
		}
		var inner strings.Builder
		lossy = renderBlocks(&inner, []adfNode{c}, indent+"  ") || lossy
		b.WriteString(inner.String())
		first = false
	}
	if first {
		b.WriteString("\n")
	}
	return lossy
}

func renderInline(b *strings.Builder, nodes []adfNode) (lossy bool) {
	for _, n := range nodes {
		switch n.Type {
		case "text":
			b.WriteString(applyMarks(n.Text, n.Marks))
		case "hardBreak":
			b.WriteString("\n")
		case "mention":
			// Jira user-mention nodes render as PLAIN names and must never
			// become agent mentions (FR-29): the bridge only rewrites
			// plain-text @Name tokens typed by humans, and this rendering
			// deliberately omits the "@" so a mentioned human named like an
			// agent cannot wake it.
			if v, ok := n.Attrs["text"].(string); ok {
				b.WriteString(strings.TrimPrefix(v, "@"))
			}
		case "emoji":
			if v, ok := n.Attrs["shortName"].(string); ok {
				b.WriteString(v)
			}
		case "inlineCard":
			if v, ok := n.Attrs["url"].(string); ok {
				b.WriteString(v)
			}
			lossy = true
		default:
			if n.Text != "" {
				b.WriteString(n.Text)
			} else if len(n.Content) > 0 {
				lossy = renderInline(b, n.Content) || lossy
				continue
			}
			lossy = true
		}
	}
	return lossy
}

func applyMarks(text string, marks []adfMark) string {
	if text == "" {
		return text
	}
	out := text
	for _, m := range marks {
		switch m.Type {
		case "strong":
			out = "**" + out + "**"
		case "em":
			out = "*" + out + "*"
		case "code":
			out = "`" + out + "`"
		case "link":
			if href, ok := m.Attrs["href"].(string); ok {
				out = "[" + out + "](" + href + ")"
			}
		case "strike":
			out = "~~" + out + "~~"
		}
	}
	return out
}

func collectText(b *strings.Builder, nodes []adfNode) {
	for _, n := range nodes {
		if n.Text != "" {
			b.WriteString(n.Text)
			b.WriteString(" ")
		}
		collectText(b, n.Content)
	}
}

// CanonicalMarkdown normalizes Markdown so snapshots compare stably (AD-5):
// CRLF→LF, trailing spaces stripped, runs of 3+ newlines collapsed to one
// blank line, and the document trimmed. Same input always yields the same
// bytes — signature stability depends on it.
func CanonicalMarkdown(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	s = strings.Join(lines, "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s)
}

// TextToADF renders text as a minimal ADF document: one paragraph per
// blank-line-separated block, hard breaks inside blocks. The full
// Markdown-profile writer replaces the body rendering in Story 3.4; the
// shape here is already valid ADF that Jira renders readably.
func TextToADF(text string) json.RawMessage {
	blocks := strings.Split(CanonicalMarkdown(text), "\n\n")
	content := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		lines := strings.Split(block, "\n")
		var inner []map[string]any
		for i, line := range lines {
			if line != "" {
				inner = append(inner, map[string]any{"type": "text", "text": line})
			}
			if i < len(lines)-1 {
				inner = append(inner, map[string]any{"type": "hardBreak"})
			}
		}
		if len(inner) == 0 {
			continue
		}
		content = append(content, map[string]any{"type": "paragraph", "content": inner})
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "paragraph", "content": []map[string]any{{"type": "text", "text": " "}}})
	}
	doc := map[string]any{"type": "doc", "version": 1, "content": content}
	raw, err := json.Marshal(doc)
	if err != nil {
		return json.RawMessage(`{"type":"doc","version":1,"content":[]}`)
	}
	return raw
}
