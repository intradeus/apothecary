package engine

import (
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/platform/filestore"
)

// An answer's files land under its ping's reference, the task's under
// ticket/, a chat's under takeover/ or ask/, each by its stored name — no #
// — a second file with the same name in one folder name-2.ext, in the order
// the files were made. The paths do not depend on the order they are passed
// in, so a re-delivery after a restart lands them where the agent was told;
// a ping the run has no reference for keeps its id.
func TestAttachmentPaths(t *testing.T) {
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	file := func(id, ping, name string, minute int) filestore.Attachment {
		return filestore.Attachment{ID: id, PingID: ping, Filename: name, CreatedAt: at.Add(time.Duration(minute) * time.Minute)}
	}
	chat := func(id, chat string, minute int) filestore.Attachment {
		a := file(id, "", "image.png", minute)
		a.Chat = chat
		return a
	}
	atts := []filestore.Attachment{
		file("t2", "", "spec.md", 2),
		file("t1", "", "spec.md", 1),
		file("p3", "p", "shot.png", 3),
		file("p1", "p", "shot.png", 1),
		file("p2", "p", "notes#1.txt", 1),
		file("q1", "q", "shot.png", 1),
		file("z1", "unknown", "", 1),
		file("pb", "p", "shot.png", 3), // made at the same moment as p3: by id
		chat("c2", filestore.ChatTakeOver, 5),
		chat("c1", filestore.ChatTakeOver, 4),
		chat("k1", filestore.ChatAsk, 4),
		file("ti", "", "image.png", 0), // the task's, beside the chats'
	}
	refs := map[string]string{"p": "build#3/decision#1", "q": "run/decision#2"}
	pingRef := func(id string) string { return refs[id] }
	want := []string{
		".apothecary/attachments/ticket/spec-2.md",
		".apothecary/attachments/ticket/spec.md",
		".apothecary/attachments/build#3/decision#1/shot-2.png",
		".apothecary/attachments/build#3/decision#1/shot.png",
		".apothecary/attachments/build#3/decision#1/notes-1.txt",
		".apothecary/attachments/run/decision#2/shot.png",
		".apothecary/attachments/unknown/file",
		".apothecary/attachments/build#3/decision#1/shot-3.png",
		".apothecary/attachments/takeover/image-2.png",
		".apothecary/attachments/takeover/image.png",
		".apothecary/attachments/ask/image.png",
		".apothecary/attachments/ticket/image.png",
	}
	got := attachmentPaths(atts, pingRef)
	if !slices.Equal(got, want) {
		t.Errorf("paths:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	reversed := slices.Clone(atts)
	slices.Reverse(reversed)
	again := attachmentPaths(reversed, pingRef)
	slices.Reverse(again)
	if !slices.Equal(again, want) {
		t.Errorf("passed in another order:\n%s", strings.Join(again, "\n"))
	}
}
