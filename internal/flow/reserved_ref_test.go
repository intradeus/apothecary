package flow

import (
	"strings"
	"testing"

	"apothecary/internal/run/ref"
)

// reservedDoc is a flow whose names and paths the cases below edit.
const reservedDoc = `name: t
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: writer
    prompt: write
    outputs:
      - { name: out, type: file, description: "what it writes" }
  - name: reader
    prompt: read
    inputs:
      - { name: out, type: file }
      - { name: doc, type: file, optional: true }
sequences:
  - id: pair
    stages:
      - { id: inner, type: command, run: "true" }
main:
  - { id: write, type: agent, harness: $harness, model: $model, agent: writer,
      output: { out: { to: artifacts/out.md, type: file } } }
  - { id: read, type: agent, harness: $harness, model: $model, agent: reader,
      input: { out: { from: artifacts/out.md, type: file } } }
  - { id: both, sequence: pair }
`

// `#` is reserved for references: a stage, sequence or port name, an output
// path or a path read from another stage holding it is refused, saying why.
// A repo: file keeps its name, whatever it holds.
func TestReferenceCharacterIsReserved(t *testing.T) {
	if ReservedRef != "#" || !strings.Contains(ref.Attempt("review", 5), ReservedRef) {
		t.Fatalf("the flow reserves %q, references use %q", ReservedRef, ref.Attempt("review", 5))
	}
	for name, edit := range map[string][2]string{
		"stage id":    {"id: read,", "id: re#ad,"},
		"sequence id": {"id: pair\n", "id: pa#ir\n"},
		"port name":   {"output: { out: { to:", "output: { o#ut: { to:"},
		"output path": {"to: artifacts/out.md", "to: artifacts/o#ut.md"},
		"input path":  {"from: artifacts/out.md", "from: artifacts/o#ut.md"},
	} {
		doc := strings.Replace(reservedDoc, edit[0], edit[1], 1)
		if doc == reservedDoc {
			t.Fatalf("%s: the edit did not apply", name)
		}
		_, err := Parse("t.yaml", []byte(doc))
		if err == nil || !strings.Contains(err.Error(), "which is reserved for references (review#5 is attempt 5 of review)") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	doc := strings.Replace(reservedDoc, "input: { out: { from: artifacts/out.md, type: file } }",
		"input: { out: { from: artifacts/out.md, type: file }, doc: { from: \"repo:docs/c#.md\", type: file, optional: true } }", 1)
	if _, err := Parse("t.yaml", []byte(doc)); err != nil {
		t.Errorf("a repo: file with # in its name: %v", err)
	}
	if _, err := Parse("t.yaml", []byte(reservedDoc)); err != nil {
		t.Fatalf("the base flow: %v", err)
	}
}
