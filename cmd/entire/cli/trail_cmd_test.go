package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/trail"
)

const (
	trailListTestAuthorAlice = "alice"
	trailListTestAuthorBob   = "bob"
)

func TestLimitTrailsKeepsMostRecentPrefix(t *testing.T) {
	trails := []*trail.Metadata{
		{Branch: "newest"},
		{Branch: "middle"},
		{Branch: "oldest"},
	}

	got := limitTrails(trails, 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Branch != "newest" || got[1].Branch != "middle" {
		t.Fatalf("got branches %q, %q; want newest, middle", got[0].Branch, got[1].Branch)
	}

	if all := limitTrails(trails, 3); len(all) != len(trails) {
		t.Fatalf("limit 3 len = %d, want %d", len(all), len(trails))
	}
}

func TestFilterTrailsByAuthor(t *testing.T) {
	alice := trailListTestAuthorAlice
	bob := trailListTestAuthorBob
	trails := []*trail.Metadata{
		{Branch: "mine-1", Author: &trail.Author{Login: &alice}},
		{Branch: "theirs", Author: &trail.Author{Login: &bob}},
		{Branch: "unknown"},
		{Branch: "mine-2", Author: &trail.Author{Login: &alice}},
	}

	got := filterTrailsByAuthor(trails, trailListTestAuthorAlice)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Branch != "mine-1" || got[1].Branch != "mine-2" {
		t.Fatalf("got branches %q, %q; want mine-1, mine-2", got[0].Branch, got[1].Branch)
	}
}

func TestParseTrailStatusFilterAcceptsCommaSeparatedStatuses(t *testing.T) {
	got, err := parseTrailStatusFilter("in_progress, open,closed")
	if err != nil {
		t.Fatalf("parseTrailStatusFilter: %v", err)
	}
	want := []trail.Status{trail.StatusInProgress, trail.StatusOpen, trail.StatusClosed}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseTrailStatusFilterRejectsInvalidStatus(t *testing.T) {
	if _, err := parseTrailStatusFilter("in_progress,nope"); err == nil {
		t.Fatal("expected invalid status error")
	}
}

func TestPrintTrailListDefaultRepoShapeShowsAuthor(t *testing.T) {
	alice := trailListTestAuthorAlice
	var out bytes.Buffer
	printTrailList(&out, []*trail.Metadata{
		{
			Branch:    "feat/repo-wide",
			Status:    trail.StatusInProgress,
			Author:    &trail.Author{Login: &alice},
			UpdatedAt: time.Now(),
		},
	}, trailListDisplayOptions{
		RequestedAuthor: "",
		StatusFilters:   []trail.Status{trail.StatusInProgress},
	})

	text := out.String()
	for _, want := range []string{"In progress · 1 trail", "feat/repo-wide", trailListTestAuthorAlice} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q, got:\n%s", want, text)
		}
	}
}

func TestPrintTrailListAuthorFilteredShapeHidesAuthor(t *testing.T) {
	longBranch := "feature/very-long-branch-name-that-must-remain-visible"
	alice := trailListTestAuthorAlice

	var out bytes.Buffer
	printTrailList(&out, []*trail.Metadata{
		{
			Branch:    longBranch,
			Status:    trail.StatusInProgress,
			Author:    &trail.Author{Login: &alice},
			UpdatedAt: time.Now().Add(-24 * time.Hour),
		},
	}, trailListDisplayOptions{
		RequestedAuthor: trailListTestAuthorAlice,
		StatusFilters:   []trail.Status{trail.StatusInProgress},
	})

	text := out.String()
	if !strings.Contains(text, "alice · 1 in progress") {
		t.Fatalf("output should contain author/status header, got:\n%s", text)
	}
	if !strings.Contains(text, longBranch) {
		t.Fatalf("output should contain full branch %q, got:\n%s", longBranch, text)
	}
	if strings.Count(text, "alice") != 1 {
		t.Fatalf("filtered author output should not repeat author in rows, got:\n%s", text)
	}
}

func TestPrintTrailListAnyAuthorAnyStatusGroupsByStatus(t *testing.T) {
	alice := trailListTestAuthorAlice
	bob := trailListTestAuthorBob
	var out bytes.Buffer
	printTrailList(&out, []*trail.Metadata{
		{Branch: "feat/a", Status: trail.StatusInProgress, Author: &trail.Author{Login: &alice}, UpdatedAt: time.Now()},
		{Branch: "fix/b", Status: trail.StatusOpen, Author: &trail.Author{Login: &bob}, UpdatedAt: time.Now()},
	}, trailListDisplayOptions{
		RequestedAuthor: "",
		StatusFilters:   nil,
	})

	text := out.String()
	if strings.Index(text, "In progress · 1") > strings.Index(text, "Open · 1") {
		t.Fatalf("expected in-progress group before open group, got:\n%s", text)
	}
	for _, want := range []string{"Recent trails · 2", "In progress · 1", "Open · 1", "feat/a", trailListTestAuthorAlice, "fix/b", trailListTestAuthorBob} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q, got:\n%s", want, text)
		}
	}
}
