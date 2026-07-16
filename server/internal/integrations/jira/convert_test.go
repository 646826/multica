package jira

import (
	"strings"
	"testing"
)

func TestADFToMarkdownProfile(t *testing.T) {
	adf := `{"type":"doc","version":1,"content":[
		{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Title"}]},
		{"type":"paragraph","content":[
			{"type":"text","text":"bold","marks":[{"type":"strong"}]},
			{"type":"text","text":" and "},
			{"type":"text","text":"code","marks":[{"type":"code"}]},
			{"type":"text","text":" and "},
			{"type":"text","text":"a link","marks":[{"type":"link","attrs":{"href":"https://example.test"}}]}
		]},
		{"type":"bulletList","content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}
		]},
		{"type":"orderedList","content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]}]}
		]},
		{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"fmt.Println(1)"}]}
	]}`
	md, lossy := ADFToMarkdown([]byte(adf))
	if lossy {
		t.Fatalf("profile document must not be lossy, got md=%q", md)
	}
	for _, want := range []string{
		"## Title",
		"**bold**",
		"`code`",
		"[a link](https://example.test)",
		"- one",
		"- two",
		"1. first",
		"```go\nfmt.Println(1)\n```",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestADFToMarkdownDegradesUnknownNodesReadably(t *testing.T) {
	adf := `{"type":"doc","version":1,"content":[
		{"type":"paragraph","content":[{"type":"text","text":"before"}]},
		{"type":"mediaSingle","content":[{"type":"media","attrs":{"id":"x"}}]},
		{"type":"weirdFutureNode","content":[{"type":"text","text":"embedded words"}]}
	]}`
	md, lossy := ADFToMarkdown([]byte(adf))
	if !lossy {
		t.Fatal("degraded document must report lossy=true")
	}
	if !strings.Contains(md, "before") || !strings.Contains(md, "[attachment]") || !strings.Contains(md, "embedded words") {
		t.Fatalf("degradation must keep readable text: %q", md)
	}
}

func TestADFMentionNodesRenderPlainWithoutAt(t *testing.T) {
	adf := `{"type":"doc","version":1,"content":[
		{"type":"paragraph","content":[
			{"type":"text","text":"ping "},
			{"type":"mention","attrs":{"id":"acc-1","text":"@Max"}},
			{"type":"text","text":" please"}
		]}
	]}`
	md, _ := ADFToMarkdown([]byte(adf))
	if strings.Contains(md, "@Max") {
		t.Fatalf("Jira mention nodes must render WITHOUT '@' (FR-29 human-mention protection): %q", md)
	}
	if !strings.Contains(md, "Max") {
		t.Fatalf("mention display name must survive: %q", md)
	}
}

func TestCanonicalMarkdownIsStable(t *testing.T) {
	in := "a  \r\nb\n\n\n\nc\t\n"
	once := CanonicalMarkdown(in)
	twice := CanonicalMarkdown(once)
	if once != twice {
		t.Fatalf("canonicalizer must be idempotent: %q vs %q", once, twice)
	}
	if strings.Contains(once, "\r") || strings.Contains(once, "\n\n\n") {
		t.Fatalf("normalization incomplete: %q", once)
	}
}

func TestADFToMarkdownNonADFInputPassesThroughAsText(t *testing.T) {
	md, lossy := ADFToMarkdown([]byte("just plain words"))
	if !lossy || !strings.Contains(md, "just plain words") {
		t.Fatalf("non-ADF input must pass through readably (lossy): %q %v", md, lossy)
	}
}
