package pgstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/platform/filestore"
	"apothecary/internal/run/event"
	"apothecary/internal/skills/skillstore"
)

func mkAttachment(id, user string, at time.Time) filestore.Attachment {
	return filestore.Attachment{ID: id, UserID: user, Filename: id + ".txt", Mime: "text/plain", Size: 3, SHA256: "sha-" + id, CreatedAt: at}
}

func TestAttachmentClaimRules(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.InsertAttachment(ctx, mkAttachment("a1", "u1", testNow)); err != nil {
		t.Fatal(err)
	}
	// Exactly one owner: neither and both are refused without touching the row.
	for _, c := range [][2]string{{"", ""}, {"t1", "r1"}} {
		err := s.ClaimAttachments(ctx, "u1", []string{"a1"}, c[0], c[1], "")
		if err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("claim ticket=%q run=%q = %v", c[0], c[1], err)
		}
	}
	if got, _ := s.GetAttachment(ctx, "a1"); got.RunID != "" || got.TicketID != "" {
		t.Errorf("refused claim wrote: %+v", got)
	}
	// Someone else's id is a silent no-op, like one that matches nothing.
	if err := s.ClaimAttachments(ctx, "u2", []string{"a1"}, "", "r1", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAttachment(ctx, "a1"); !got.Unclaimed() {
		t.Errorf("another user claimed it: %+v", got)
	}
	// A ticket that does not exist trips the FK backstop.
	if err := s.ClaimAttachments(ctx, "u1", []string{"a1"}, "ghost", "", ""); err == nil {
		t.Error("claim onto a missing ticket succeeded")
	}
	// A ping file names its run; an orphan row cannot say otherwise.
	bad := mkAttachment("a2", "u1", testNow)
	bad.PingID = "p1"
	if err := s.InsertAttachment(ctx, bad); err == nil {
		t.Error("ping attachment without a run stored")
	}
	// Ids that match nothing are a silent no-op.
	if err := s.ClaimAttachments(ctx, "u1", []string{"ghost"}, "", "r1", ""); err != nil {
		t.Errorf("claim of nothing = %v", err)
	}
}

func TestAttachmentSweepHelpers(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	old := testNow
	young := testNow.Add(2 * time.Hour)
	for _, a := range []filestore.Attachment{mkAttachment("a1", "u1", old), mkAttachment("a2", "u2", old), mkAttachment("a3", "u1", young), mkAttachment("a4", "u1", old)} {
		if err := s.InsertAttachment(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ClaimAttachments(ctx, "u1", []string{"a4"}, "", "r1", ""); err != nil {
		t.Fatal(err)
	}
	cutoff := testNow.Add(time.Hour)
	all, err := s.UnclaimedAttachmentIDsBefore(ctx, "", cutoff)
	slices.Sort(all)
	if err != nil || !slices.Equal(all, []string{"a1", "a2"}) {
		t.Errorf("unclaimed = %v, %v", all, err)
	}
	// Per-user retention sees only that user's uploads.
	mine, err := s.UnclaimedAttachmentIDsBefore(ctx, "u1", cutoff)
	if err != nil || !slices.Equal(mine, []string{"a1"}) {
		t.Errorf("u1 unclaimed = %v, %v", mine, err)
	}
	// Created exactly at the cutoff is not yet before it.
	if at, _ := s.UnclaimedAttachmentIDsBefore(ctx, "u2", old); len(at) != 0 {
		t.Errorf("cutoff is exclusive: %v", at)
	}

	if err := s.DeleteAttachments(ctx, nil); err != nil {
		t.Errorf("delete nothing = %v", err)
	}
	if err := s.DeleteAttachments(ctx, []string{"a1", "a2", "ghost"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "a2"} {
		if _, err := s.GetAttachment(ctx, id); !errors.Is(err, filestore.ErrNotFound) {
			t.Errorf("%s survived: %v", id, err)
		}
	}
	if _, err := s.GetAttachment(ctx, "a3"); err != nil {
		t.Errorf("a3 went too: %v", err)
	}
}

func TestSnapshotGetAndKinds(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.GetSnapshot(ctx, "nope"); !errors.Is(err, filestore.ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	// A log has neither role nor path; empty strings must map to NULL or the
	// CHECKs refuse it.
	lg := filestore.Snapshot{ID: "s1", RunID: "r1", Kind: filestore.KindLog, StageID: "build", Iteration: 2,
		Filename: "build.log", Mime: "text/plain", Size: 9, SHA256: "aa", Truncated: true, CreatedAt: testNow}
	if err := s.InsertSnapshot(ctx, lg); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSnapshot(ctx, "s1")
	if err != nil || got.Kind != filestore.KindLog || got.Name != "" || got.Path != "" || got.PingID != "" ||
		!got.Truncated || got.Iteration != 2 || !got.CreatedAt.Equal(testNow) {
		t.Fatalf("log = %+v, %v", got, err)
	}
	pingless := filestore.Snapshot{ID: "s2", RunID: "r1", Kind: filestore.KindPingFile, StageID: "b", Path: "p",
		Filename: "f", Mime: "m", SHA256: "bb", CreatedAt: testNow}
	if err := s.InsertSnapshot(ctx, pingless); err == nil {
		t.Error("a ping file without a ping stored")
	}
}

// A snapshot stops pinning bytes once past ITS OWNER's window; another
// owner's retention says nothing about it, and attachments are not on a
// window at all.
func TestLiveBlobSHAsRetentionIsPerOwner(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for run, user := range map[string]string{"r1": "u1", "r2": "u2"} {
		if _, err := s.Append(ctx, run, &event.RunCreated{UserID: user}); err != nil {
			t.Fatal(err)
		}
	}
	snap := func(id, run, sha string, at time.Time) filestore.Snapshot {
		return filestore.Snapshot{ID: id, RunID: run, Kind: filestore.KindLog, StageID: "b",
			Filename: "f", Mime: "m", SHA256: sha, CreatedAt: at}
	}
	for _, sn := range []filestore.Snapshot{
		snap("s1", "r1", "old-u1", testNow),
		snap("s2", "r1", "new-u1", testNow.Add(2*time.Hour)),
		snap("s3", "r2", "old-u2", testNow),
		snap("s4", "gone-run", "orphan", testNow), // no runs row: owner ""
	} {
		if err := s.InsertSnapshot(ctx, sn); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertAttachment(ctx, mkAttachment("a1", "u1", testNow)); err != nil {
		t.Fatal(err)
	}
	cutoff := testNow.Add(time.Hour)
	live, err := s.LiveBlobSHAs(ctx, map[string]time.Time{"u1": cutoff})
	if err != nil {
		t.Fatal(err)
	}
	if live["old-u1"] {
		t.Error("u1's expired snapshot still pins its bytes")
	}
	if !live["new-u1"] || !live["old-u2"] || !live["orphan"] || !live["sha-a1"] {
		t.Errorf("live = %v", live)
	}
	// "" is the owner of a run-less snapshot; retention can reach it too.
	live, _ = s.LiveBlobSHAs(ctx, map[string]time.Time{"": cutoff})
	if live["orphan"] || !live["old-u1"] {
		t.Errorf("owner-less cutoff: %v", live)
	}
}

// A skillset pinned by a live run cannot be deleted: the pin is in the JSON
// view, so the check is a containment query. A finished run lets go.
func TestDeleteSkillsetRefusesLivePin(t *testing.T) {
	s := openSkills(t)
	ctx := context.Background()
	set := skillstore.Skillset{ID: "set1", UserID: "u1", Name: "Set", CreatedAt: testNow, UpdatedAt: testNow}
	other := skillstore.Skillset{ID: "set2", UserID: "u1", Name: "Other", CreatedAt: testNow, UpdatedAt: testNow}
	for _, x := range []skillstore.Skillset{set, other} {
		if err := s.UpsertSkillset(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Append(ctx, "r1", &event.RunCreated{Skillsets: []event.SkillsetPin{{ID: "set1", Seq: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSkillset(ctx, "set1"); !errors.Is(err, errReferenced()) || !strings.Contains(err.Error(), "r1") {
		t.Fatalf("pinned delete = %v", err)
	}
	// The containment match is on the id, not on any pin.
	if err := s.DeleteSkillset(ctx, "set2"); err != nil {
		t.Errorf("unpinned delete = %v", err)
	}
	if _, err := s.Append(ctx, "r1", &event.RunCompleted{}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSkillset(ctx, "set1"); err != nil {
		t.Errorf("finished run still pins: %v", err)
	}
}

func TestSkillsetListAndVersionEdges(t *testing.T) {
	s := openSkills(t)
	ctx := context.Background()
	src := &skillstore.Source{Kind: "archive", URL: "https://a.local/x.tgz", FetchedAt: testNow}
	for _, x := range []skillstore.Skillset{
		{ID: "s2", UserID: "u1", Name: "beta", Source: src, CreatedAt: testNow, UpdatedAt: testNow},
		{ID: "s1", UserID: "u1", Name: "alpha", CreatedAt: testNow, UpdatedAt: testNow},
		{ID: "s3", UserID: "u2", Name: "alpha", CreatedAt: testNow, UpdatedAt: testNow},
	} {
		if err := s.UpsertSkillset(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	mine, err := s.ListSkillsets(ctx, "u1")
	if err != nil || len(mine) != 2 || mine[0].ID != "s1" || mine[1].ID != "s2" {
		t.Fatalf("u1 = %+v, %v", mine, err)
	}
	if mine[1].Source == nil || mine[1].Source.URL != src.URL || mine[0].Source != nil {
		t.Errorf("sources = %+v / %+v", mine[0].Source, mine[1].Source)
	}
	if mine[0].SkillIDs == nil {
		t.Error("an empty membership must read as [], not null")
	}
	if all, err := s.ListSkillsets(ctx, ""); err != nil || len(all) != 3 {
		t.Errorf("all = %+v, %v", all, err)
	}
	if _, err := s.GetSkillset(ctx, "ghost"); !errors.Is(err, errNotFound()) {
		t.Errorf("missing = %v", err)
	}
	// A version of a set that does not exist is invalid.
	if err := s.InsertVersion(ctx, skillstore.Version{SkillsetID: "ghost", Seq: 1, Hash: "h", CreatedAt: testNow}); !errors.Is(err, errInvalid()) {
		t.Errorf("version of nothing = %v", err)
	}
	// nil members store as [] and read back as [].
	if err := s.InsertVersion(ctx, skillstore.Version{SkillsetID: "s1", Seq: 3, Hash: "h", CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetVersion(ctx, "s1", 3)
	if err != nil || got.Members == nil || len(got.Members) != 0 {
		t.Errorf("empty version = %+v, %v", got, err)
	}
	// head_seq only moves forward: backfilling an old seq leaves it alone.
	if err := s.InsertVersion(ctx, skillstore.Version{SkillsetID: "s1", Seq: 2, Hash: "h2", CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if set, _ := s.GetSkillset(ctx, "s1"); set.HeadSeq != 3 {
		t.Errorf("head_seq = %d, want 3", set.HeadSeq)
	}
	if vs, err := s.ListVersions(ctx, "ghost"); err != nil || len(vs) != 0 {
		t.Errorf("versions of nothing = %+v, %v", vs, err)
	}
}

// A skill's source is matched per owner: two people can import one URL.
func TestSkillSourceIsPerOwner(t *testing.T) {
	s := openSkills(t)
	ctx := context.Background()
	url := "https://a.local/skill.tgz"
	mk := func(id, user string) skillstore.Skill {
		return skillstore.Skill{ID: id, UserID: user, Name: "n", Hash: "h", Source: &skillstore.Source{Kind: "archive", URL: url, FetchedAt: testNow}, CreatedAt: testNow, UpdatedAt: testNow}
	}
	for _, sk := range []skillstore.Skill{mk("s1", "u1"), mk("s2", "u2")} {
		if err := s.UpsertSkill(ctx, sk); err != nil {
			t.Fatal(err)
		}
	}
	for user, want := range map[string]string{"u1": "s1", "u2": "s2"} {
		if got, err := s.GetSkillBySource(ctx, user, url); err != nil || got.ID != want {
			t.Errorf("%s by source = %+v, %v", user, got, err)
		}
	}
	if _, err := s.GetSkillBySource(ctx, "u3", url); !errors.Is(err, errNotFound()) {
		t.Errorf("stranger by source = %v", err)
	}
	if sk, err := s.GetSkill(ctx, "s1"); err != nil || sk.Files != nil && len(sk.Files) != 0 {
		t.Errorf("nil files read back = %+v, %v", sk.Files, err)
	}
}

// The sweep's mark set must survive a version whose member has no file list:
// one such row fails the whole query and stops every reclaim.
func TestLiveBlobSHAsSurvivesAMemberWithoutFiles(t *testing.T) {
	s := openSkills(t)
	ctx := context.Background()
	if err := s.UpsertSkillset(ctx, skillstore.Skillset{ID: "set1", UserID: "u1", Name: "Set", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertVersion(ctx, skillstore.Version{SkillsetID: "set1", Seq: 1, Hash: "h", CreatedAt: testNow,
		Members: []skillstore.VersionMember{{SkillID: "s1", Name: "alpha"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LiveBlobSHAs(ctx, nil); err != nil {
		t.Errorf("LiveBlobSHAs = %v", err)
	}
	// Nor can a skill with no files, or a version with no members.
	if err := s.UpsertSkill(ctx, skillstore.Skill{ID: "s2", UserID: "u1", Name: "empty", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertVersion(ctx, skillstore.Version{SkillsetID: "set1", Seq: 2, Hash: "h2", CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LiveBlobSHAs(ctx, nil); err != nil {
		t.Errorf("LiveBlobSHAs with nothing listed = %v", err)
	}
}
