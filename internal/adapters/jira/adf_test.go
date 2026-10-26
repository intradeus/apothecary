package jira

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToMarkdownWikiString(t *testing.T) {
	// API v2 (Data Center) serves the description as a plain string.
	if got := ToMarkdown(json.RawMessage(`"  h2. Title\n\nbody  "`)); got != "h2. Title\n\nbody" {
		t.Errorf("v2 string = %q", got)
	}
	if got := ToMarkdown(json.RawMessage(`null`)); got != "" {
		t.Errorf("null description = %q", got)
	}
	if got := ToMarkdown(nil); got != "" {
		t.Errorf("absent description = %q", got)
	}
}

func TestToMarkdownADF(t *testing.T) {
	doc := `{"type":"doc","version":1,"content":[
	  {"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Context"}]},
	  {"type":"paragraph","content":[
	    {"type":"text","text":"The "},
	    {"type":"text","text":"greeter","marks":[{"type":"code"}]},
	    {"type":"text","text":" is "},
	    {"type":"text","text":"broken","marks":[{"type":"strong"}]},
	    {"type":"text","text":" — see "},
	    {"type":"text","text":"the docs","marks":[{"type":"link","attrs":{"href":"https://x.test/d"}},{"type":"em"}]},
	    {"type":"text","text":"."}
	  ]},
	  {"type":"bulletList","content":[
	    {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]}]},
	    {"type":"listItem","content":[
	      {"type":"paragraph","content":[{"type":"text","text":"second"}]},
	      {"type":"bulletList","content":[
	        {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"nested"}]}]}
	      ]}
	    ]}
	  ]},
	  {"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"fmt.Println(1)"}]},
	  {"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"quoted"}]}]},
	  {"type":"rule"},
	  {"type":"mediaSingle","content":[{"type":"media","attrs":{"id":"abc","alt":"screenshot.png"}}]}
	]}`
	want := "## Context\n\n" +
		"The `greeter` is **broken** — see [*the docs*](https://x.test/d).\n\n" +
		"- first\n- second\n  - nested\n\n" +
		"```go\nfmt.Println(1)\n```\n\n" +
		"> quoted\n\n" +
		"---\n\n" +
		"[attachment: screenshot.png]"
	if got := ToMarkdown(json.RawMessage(doc)); got != want {
		t.Errorf("markdown =\n%s\n\nwant\n%s", got, want)
	}
}

func TestToMarkdownTableAndOrderedList(t *testing.T) {
	doc := `{"type":"doc","content":[
	  {"type":"orderedList","content":[
	    {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},
	    {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}
	  ]},
	  {"type":"table","content":[
	    {"type":"tableRow","content":[
	      {"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"key"}]}]},
	      {"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"value"}]}]}
	    ]},
	    {"type":"tableRow","content":[
	      {"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},
	      {"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}
	    ]}
	  ]}
	]}`
	want := "1. one\n2. two\n\n| key | value |\n| --- | --- |\n| a | b |"
	if got := ToMarkdown(json.RawMessage(doc)); got != want {
		t.Errorf("markdown =\n%s\n\nwant\n%s", got, want)
	}
}

// An unknown node type must degrade to its children rather than dropping the
// content: a description that renders imperfectly beats one that loses a
// paragraph silently.
func TestToMarkdownUnknownNodeKeepsText(t *testing.T) {
	doc := `{"type":"doc","content":[
	  {"type":"someFutureBlock","content":[
	    {"type":"paragraph","content":[{"type":"text","text":"still here"}]}
	  ]}
	]}`
	if got := ToMarkdown(json.RawMessage(doc)); got != "still here" {
		t.Errorf("unknown node = %q", got)
	}
}

// A markdown image becomes a link to it: ADF embeds media by Media API id,
// which a URL is not.
func TestFromMarkdownImageIsALink(t *testing.T) {
	doc, _ := json.Marshal(FromMarkdown("See ![home](https://x/home.png)."))
	if got := string(doc); !strings.Contains(got, `{"marks":[{"attrs":{"href":"https://x/home.png"},"type":"link"}],"text":"home","type":"text"}`) || strings.Contains(got, "!") {
		t.Errorf("adf = %s", got)
	}
}
