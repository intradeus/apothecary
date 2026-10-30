package engine

// files ports and attachments: a directory a stage fills, checked, prompted
// and captured; and create_mr / ticket_comment uploading what one holds.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/filestore"
	"apothecary/internal/ports"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
)

// png is enough of a PNG for content sniffing, with bytes no text scrub or
// truncation may touch.
var png = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\xff\xfe"

const shotsDoc = `name: shots
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: shooter
    prompt: take screenshots
    inputs:
      - { name: failure, type: failure, optional: true }
    outputs:
      - { name: shots, type: files, description: screenshots of each page }
  - name: viewer
    prompt: look at them
    inputs:
      - { name: shots, type: files }
main:
  - { id: stale, type: command, run: leave-a-stale-shot }
  - { id: shoot, type: agent, harness: $harness, model: $model, agent: shooter, input: { failure: { from: edge.failure, type: failure, optional: true } },
      output: { shots: { to: artifacts/shots/, type: files } }, on_fail: { to: shoot, max: 1 } }
  - { id: view, type: agent, harness: $harness, model: $model, agent: viewer, input: { shots: { from: artifacts/shots/, type: files } } }
`

// A files output is emptied before an attempt — a file an earlier stage
// left there does not count — and an attempt that leaves it empty fails
// the output contract. What it does write is listed to the reader, by
// name, and captured file by file, bytes intact.
func TestFilesOutputLeftEmptyFailsTheStage(t *testing.T) {
	att, snaps, _ := newFiles(t)
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Attachments, d.Snapshots = att, snaps }, mustFlow(t, shotsDoc))
	h.envp.ExecFunc = func(e *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if cmd.Script == "leave-a-stale-shot" {
			e.Files["artifacts/shots/stale.png"] = []byte(png)
		}
		return envport.ExecResult{}, nil
	}
	var mu sync.Mutex
	var shooter []string
	var viewer string
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"shooter": func(c fake.RunCall) (agentport.AgentResult, error) {
			mu.Lock()
			shooter = append(shooter, c.In.Prompt)
			n := len(shooter)
			mu.Unlock()
			if n == 2 {
				h.writeFile(t, c.Env, "artifacts/shots/home.png", png)
			}
			return agentport.AgentResult{}, nil
		},
		"viewer": func(c fake.RunCall) (agentport.AgentResult, error) {
			mu.Lock()
			viewer = c.In.Prompt
			mu.Unlock()
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "shots", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events %v", v.State, eventTypes(h.events(t, runID)))
	}
	fails := stageFailures(h.events(t, runID))
	if len(fails) != 1 || fails[0].StageID != "shoot" || fails[0].Reason != event.ReasonOutputContract ||
		!strings.Contains(fails[0].Error, "artifacts/shots/ (no file in it)") {
		t.Fatalf("failures = %+v", fails)
	}
	if len(shooter) != 2 || !strings.Contains(shooter[0], "artifacts/shots/ is a directory: write one or more files into it") {
		t.Errorf("the contract does not say it is a directory:\n%s", shooter[0])
	}
	if !strings.Contains(viewer, "(files: artifacts/shots/)\n- artifacts/shots/home.png (18 B)\n") || strings.Contains(viewer, "stale.png") {
		t.Errorf("the reader's listing:\n%s", viewer)
	}
	var captured []filestore.Snapshot
	for _, sn := range snapshotsFor(t, snaps, runID) {
		if sn.StageID == "shoot" && sn.Kind == filestore.KindOutput {
			captured = append(captured, sn)
		}
	}
	if len(captured) != 1 || captured[0].Name != "shots" || captured[0].Path != "artifacts/shots/home.png" ||
		captured[0].Iteration != 2 || captured[0].Mime != "image/png" || snapshotBody(t, snaps, captured[0].ID) != png {
		t.Errorf("snapshots = %+v", captured)
	}
}

// A pass that loops back does not add to the last one's files: the second
// attempt starts from an empty directory, so a reader — the merge request's
// attachments among them — sees only what the attempt that succeeded wrote.
func TestFilesOutputIsEmptiedBetweenPasses(t *testing.T) {
	att, snaps, _ := newFiles(t)
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Attachments, d.Snapshots = att, snaps }, mustFlow(t, shotsDoc))
	var mu sync.Mutex
	var passes int
	var viewer string
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"shooter": func(c fake.RunCall) (agentport.AgentResult, error) {
			mu.Lock()
			passes++
			n := passes
			mu.Unlock()
			if n == 1 {
				h.writeFile(t, c.Env, "artifacts/shots/sc1.png", png)
				h.writeFile(t, c.Env, "artifacts/shots/sc2.png", png)
				return agentport.AgentResult{}, errors.New("the page did not load")
			}
			h.writeFile(t, c.Env, "artifacts/shots/sc3.png", png)
			h.writeFile(t, c.Env, "artifacts/shots/sc4.png", png)
			return agentport.AgentResult{}, nil
		},
		"viewer": func(c fake.RunCall) (agentport.AgentResult, error) {
			mu.Lock()
			viewer = c.In.Prompt
			mu.Unlock()
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "shots", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed || passes != 2 {
		t.Fatalf("state = %s after %d passes, events %v", v.State, passes, eventTypes(h.events(t, runID)))
	}
	want := "(files: artifacts/shots/)\n- artifacts/shots/sc3.png (18 B)\n- artifacts/shots/sc4.png (18 B)\n"
	if !strings.Contains(viewer, want) || strings.Contains(viewer, "sc1.png") || strings.Contains(viewer, "sc2.png") {
		t.Errorf("the reader's listing:\n%s", viewer)
	}
	var second []string
	for _, sn := range snapshotsFor(t, snaps, runID) {
		if sn.StageID == "shoot" && sn.Kind == filestore.KindOutput && sn.Iteration == 2 {
			second = append(second, sn.Path)
		}
	}
	if strings.Join(second, ",") != "artifacts/shots/sc3.png,artifacts/shots/sc4.png" {
		t.Errorf("the second pass captured %v", second)
	}
}

const attachDoc = `name: at
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file },
      attachments: { from: repo:artifacts/shots/, type: files, optional: true } } }
`

// create_mr uploads each file of its attachments port and links it: a link
// or image target naming the file's path points at the upload, a bare path
// becomes the upload's markdown, and the files the body never names are
// listed after it.
func TestCreateMRUploadsAndLinksAttachments(t *testing.T) {
	h := newHarness(t, mustFlow(t, attachDoc))
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# Add the home page\n\n![home](artifacts/shots/home.png)\n\nLogging in: `./artifacts/shots/login.png`.\n")
			for _, f := range []string{"home.png", "login.png", "extra.png"} {
				h.writeFile(t, c.Env, "artifacts/shots/"+f, png)
			}
			h.writeFile(t, c.Env, "artifacts/shots/notes.txt", "timings")
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "at", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events %v", v.State, eventTypes(h.events(t, runID)))
	}
	ups := h.repoHost.SnapshotUploads()
	var got []string
	for _, u := range ups {
		got = append(got, u.Name+" "+u.ContentType)
	}
	if strings.Join(got, ", ") != "extra.png image/png, home.png image/png, login.png image/png, notes.txt text/plain" || string(ups[0].Data) != png {
		t.Fatalf("uploads = %v", got)
	}
	mrs := h.repoHost.SnapshotMRs()
	if len(mrs) != 1 {
		t.Fatalf("%d merge requests", len(mrs))
	}
	want := "# Add the home page\n\n![home](https://gitlab.local/uploads/2/home.png)\n\n" +
		"Logging in: ![login.png](https://gitlab.local/uploads/3/login.png).\n\n" +
		"## Screenshots\n\n![extra.png](https://gitlab.local/uploads/1/extra.png)\n\n" +
		"## Attachments\n\n- [notes.txt](https://gitlab.local/uploads/4/notes.txt)\n"
	if mrs[0].Body != want {
		t.Errorf("body =\n%s\nwant\n%s", mrs[0].Body, want)
	}
	if mrs[0].Title != "Add the home page" {
		t.Errorf("title = %q", mrs[0].Title)
	}
}

// An optional attachments port with nothing there is no attachment: the
// merge request opens with its body as written.
func TestCreateMRWithoutAttachmentsKeepsTheBody(t *testing.T) {
	h := newHarness(t, mustFlow(t, attachDoc))
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# Fix\n\nNo screenshots.")
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "at", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events %v", v.State, eventTypes(h.events(t, runID)))
	}
	if n := len(h.repoHost.SnapshotUploads()); n != 0 {
		t.Errorf("%d uploads", n)
	}
	if mrs := h.repoHost.SnapshotMRs(); len(mrs) != 1 || mrs[0].Body != "# Fix\n\nNo screenshots." {
		t.Errorf("merge requests = %+v", mrs)
	}
}

// Past the limits the files are refused before anything goes up, for
// on_fail to route.
func TestCreateMRRefusesTooManyAttachments(t *testing.T) {
	h := newHarness(t, mustFlow(t, attachDoc))
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# Fix\n")
			for i := range attachmentMaxFiles + 1 {
				h.writeFile(t, c.Env, "artifacts/shots/"+string(rune('a'+i))+".png", png)
			}
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "at", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	fails := stageFailures(h.events(t, runID))
	if len(fails) != 1 || fails[0].StageID != "mr" || fails[0].Reason != event.ReasonInputInvalid || !strings.Contains(fails[0].Error, "holds 21 files; at most 20") {
		t.Fatalf("failures = %+v", fails)
	}
	if len(h.repoHost.SnapshotUploads()) != 0 || len(h.repoHost.SnapshotMRs()) != 0 {
		t.Error("something went up past the limit")
	}
}

// ticket_comment attaches its files to the tracker issue and links them in
// the comment, ahead of the trailer.
func TestTicketCommentAttachesFiles(t *testing.T) {
	doc := `name: cm
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: write, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/ticket_comment.md, type: file } } }
  - { id: post,  type: action, action: ticket_comment, input: { body: { from: artifacts/ticket_comment.md, type: file },
      attachments: { from: repo:artifacts/shots/, type: files } } }
`
	h, tracker := trackerHarness(t, doc)
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/ticket_comment.md", "Deployed: artifacts/shots/home.png\n")
			h.writeFile(t, c.Env, "artifacts/shots/home.png", png)
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "cm", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, runID)))
	}
	want := "Deployed: [home.png](https://tracker.local/DEV-1/attachments/1/home.png)\n\n— apothecary · run " + runID + "/main-1 · stage post"
	if posted := tracker.Posted["DEV-1"]; len(posted) != 1 || posted[0] != want {
		t.Errorf("posted = %q, want %q", posted, want)
	}
	if len(tracker.Attached) != 1 || tracker.Attached[0].ContentType != "image/png" {
		t.Errorf("attached = %+v", tracker.Attached)
	}
}

func TestLinkAttachmentsRules(t *testing.T) {
	img := func(p, url string) linkedFile {
		return linkedFile{path: p, name: p[strings.LastIndex(p, "/")+1:], image: true, att: ports.Attachment{URL: url}}
	}
	for _, tc := range []struct {
		name, body, want string
	}{
		{"link target with a title", `[see](./a/x.png "shot")`, `[see](U "shot")` + "\n"},
		{"a longer path is another file", "a/x.png.bak and a/x.pngs", "a/x.png.bak and a/x.pngs\n\n## Screenshots\n\n![x.png](U)\n"},
		{"sentence end", "Here: a/x.png.", "Here: ![x.png](U).\n"},
		{"label left alone", "[a/x.png](a/x.png)", "[a/x.png](U)\n"},
		{"a path under another dir", "b/a/x.png", "b/a/x.png\n\n## Screenshots\n\n![x.png](U)\n"},
		{"twice", "a/x.png, `a/x.png`", "![x.png](U), ![x.png](U)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := linkAttachments(tc.body, []linkedFile{img("a/x.png", "U")}); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	if got := linkAttachments("[x](a/x.png)", []linkedFile{img("a/x.png", "https://h/$1")}); got != "[x](https://h/$1)\n" {
		t.Errorf("a $ in the target was expanded: %q", got)
	}
	// A host's own short link is what a target points at; its markdown is
	// what a bare mention becomes.
	f := linkedFile{path: "a/x.png", name: "x.png", image: true,
		att: ports.Attachment{URL: "https://h/-/project/1/uploads/s/x.png", Link: "/uploads/s/x.png", Markdown: "![x](/uploads/s/x.png)"}}
	if got := linkAttachments("![x](a/x.png) a/x.png", []linkedFile{f}); got != "![x](/uploads/s/x.png) ![x](/uploads/s/x.png)\n" {
		t.Errorf("got %q", got)
	}
}
