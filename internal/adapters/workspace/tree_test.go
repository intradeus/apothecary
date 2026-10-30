package workspace

import (
	"apothecary/internal/ports/envport"
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestWriteTreeSendsScriptAndTar(t *testing.T) {
	r := &scriptRunner{responses: []response{{code: 0}}}
	a := newTestAdapter(t, r)
	files := []envport.TreeFile{
		{Path: "scripts/run.sh", Mode: 0o755, Data: []byte("#!/bin/sh\necho hi\n")},
		{Path: "SKILL.md", Data: []byte("---\nname: x\n---\nbody\n")},
	}
	if err := a.WriteTree(context.Background(), "ws", "/apothecary/skills/x", files); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(r.calls))
	}
	stdin := string(r.calls[0].stdin)
	line, rest, _ := strings.Cut(stdin, "\n")
	want := "rm -rf '/apothecary/skills/x' && mkdir -p '/apothecary/skills/x' && base64 -d | tar -xf - -C '/apothecary/skills/x'"
	if line != want {
		t.Errorf("script line = %q\nwant %q", line, want)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	var got []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		got = append(got, fmt.Sprintf("%s:%o:%d", hdr.Name, hdr.Mode, len(data)))
	}
	if wantEntries := []string{"SKILL.md:644:21", "scripts/run.sh:755:18"}; !reflect.DeepEqual(got, wantEntries) {
		t.Errorf("tar entries = %v, want %v", got, wantEntries)
	}
}

func TestWriteTreeRejectsBadInput(t *testing.T) {
	r := &scriptRunner{responses: []response{{code: 0}}}
	a := newTestAdapter(t, r)
	ctx := context.Background()
	if err := a.WriteTree(ctx, "ws", "relative/dir", nil); err == nil {
		t.Error("relative dir accepted")
	}
	for _, p := range []string{"", "/abs", "../x", "a/../b", "a//b", "a/./b", "a\\b", "a\x01b"} {
		if err := a.WriteTree(ctx, "ws", "/apothecary/skills/x", []envport.TreeFile{{Path: p}}); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
	dup := []envport.TreeFile{{Path: "a"}, {Path: "a"}}
	if err := a.WriteTree(ctx, "ws", "/apothecary/skills/x", dup); err == nil {
		t.Error("duplicate path accepted")
	}
	if len(r.calls) != 0 {
		t.Errorf("rejected input still reached the runner: %d calls", len(r.calls))
	}
}

func TestWriteTreeReportsExitCode(t *testing.T) {
	r := &scriptRunner{responses: []response{{code: 2, stderr: "tar: not found"}}}
	a := newTestAdapter(t, r)
	err := a.WriteTree(context.Background(), "ws", "/apothecary/skills/x", []envport.TreeFile{{Path: "SKILL.md"}})
	if err == nil || !strings.Contains(err.Error(), "exit 2") || !strings.Contains(err.Error(), "tar: not found") {
		t.Errorf("err = %v", err)
	}
}
