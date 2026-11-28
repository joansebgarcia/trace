package ingest_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joansgarcia/trace/event"
	"github.com/joansgarcia/trace/hub"
	"github.com/joansgarcia/trace/issues"
)

func TestIssueKeepsItsAggregates(t *testing.T) {
	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)
	failure := exc("TypeError", "x", f("render", "src/app.js", 1, true))
	sent := []string{
		eventWith(1, failure, `"level": "warning"`, `"user": {"id": "ann"}`),
		eventWith(2, failure, `"level": "fatal"`, `"user": {"id": "bob"}`, `"environment": "staging"`),
		eventWith(3, failure, `"user": {"id": "ann"}`, `"release": "checkout@2.0.0"`),
		eventWith(4, failure),
	}
	for _, event := range sent {
		postEvents(t, e, key, event)
		waitPending(t, e.Queue, 0)
	}

	got := listIssues(t, e, project.ID)
	if len(got) != 1 {
		t.Fatalf("Issues = %d, want 1 across Environments and Releases", len(got))
	}
	issue := got[0]
	if issue.EventCount != 4 || issue.Level != event.LevelFatal || issue.UserCount != 2 {
		t.Errorf("issue = %d Events, level %s, %d Users; want 4, fatal, 2", issue.EventCount, issue.Level, issue.UserCount)
	}
	stored := listEvents(t, e, project.ID)
	first, last := stored[0].ReceivedAt, stored[len(stored)-1].ReceivedAt
	if !issue.FirstSeen.Equal(first) || !issue.LastSeen.Equal(last) || !last.After(first) {
		t.Errorf("seen = %s – %s, want the received_at of the first and last Events, %s – %s", issue.FirstSeen, issue.LastSeen, first, last)
	}
	if issue.Status != issues.StatusOpen || issue.Title != "TypeError: x" || issue.Culprit != "render @ src/app.js" {
		t.Errorf("issue = %+v, want open with the title and culprit of its first Event", issue)
	}
}

func TestCulpritOfAnAnonymousFunctionIsItsFile(t *testing.T) {
	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)

	postEvents(t, e, key, eventWith(1, exc("TypeError", "x",
		f("dispatch", "node_modules/react/index.js", 1, false),
		f("<anonymous>", "src/app.js", 2, true))))
	waitPending(t, e.Queue, 0)

	if got := listIssues(t, e, project.ID)[0].Culprit; got != "src/app.js" {
		t.Errorf("culprit = %q, want src/app.js", got)
	}
}

func TestResentEventDoesNotCountAgain(t *testing.T) {
	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)
	sent := eventWith(1, exc("TypeError", "x"), `"user": {"id": "ann"}`)

	for range 2 {
		if status, body := postEvents(t, e, key, sent); status != 202 || strings.TrimSpace(body) != `{"accepted":1}` {
			t.Fatalf("response = %d %s, want 202 {\"accepted\":1}", status, body)
		}
		waitPending(t, e.Queue, 0)
	}

	got := listIssues(t, e, project.ID)
	if len(got) != 1 || got[0].EventCount != 1 || got[0].UserCount != 1 {
		t.Errorf("issues = %+v, want 1 with 1 Event and 1 User", got)
	}
}

func TestEventForAResolvedIssueIsARegression(t *testing.T) {
	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)
	postEvents(t, e, key, eventWith(1, exc("TypeError", "x")))
	waitPending(t, e.Queue, 0)
	resolved := listIssues(t, e, project.ID)[0]
	setStatus(t, e, resolved.ID, issues.StatusResolved)
	changes := subscribe(t, e.Hub)

	postEvents(t, e, key, eventWith(2, exc("TypeError", "x")))
	waitPending(t, e.Queue, 0)

	got := listIssues(t, e, project.ID)
	if len(got) != 1 || got[0].ID != resolved.ID {
		t.Fatalf("issues = %+v, want the resolved one", got)
	}
	issue := got[0]
	if issue.Status != issues.StatusOpen || issue.EventCount != 2 {
		t.Errorf("issue = %s with %d Events, want open with 2", issue.Status, issue.EventCount)
	}
	if issue.RegressedAt.IsZero() || !issue.StatusChangedAt.Equal(issue.RegressedAt) || !issue.RegressedAt.Equal(issue.LastSeen) {
		t.Errorf("regressed_at = %s, status_changed_at = %s, want both at the last Event, %s", issue.RegressedAt, issue.StatusChangedAt, issue.LastSeen)
	}
	waitChange(t, changes, hub.Change{Kind: hub.IssueRegressed, ProjectID: project.ID, IssueID: issue.ID})
}

func TestIgnoredIssueKeepsCountingWithoutRegression(t *testing.T) {
	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)
	postEvents(t, e, key, eventWith(1, exc("TypeError", "x"), `"level": "warning"`))
	waitPending(t, e.Queue, 0)
	ignored := listIssues(t, e, project.ID)[0]
	setStatus(t, e, ignored.ID, issues.StatusIgnored)
	ignored = listIssues(t, e, project.ID)[0]
	changes := subscribe(t, e.Hub)

	postEvents(t, e, key, eventWith(2, exc("TypeError", "x"), `"level": "fatal"`))
	waitPending(t, e.Queue, 0)

	issue := listIssues(t, e, project.ID)[0]
	if issue.Status != issues.StatusIgnored || issue.EventCount != 2 || issue.Level != event.LevelFatal {
		t.Errorf("issue = %s with %d Events, level %s; want ignored with 2, fatal", issue.Status, issue.EventCount, issue.Level)
	}
	if !issue.RegressedAt.IsZero() || !issue.StatusChangedAt.Equal(ignored.StatusChangedAt) {
		t.Errorf("regressed_at = %s, status_changed_at = %s; want no Regression", issue.RegressedAt, issue.StatusChangedAt)
	}
	waitChange(t, changes, hub.Change{Kind: hub.IssueUpdated, ProjectID: project.ID, IssueID: issue.ID})
}

func TestFirstEventOfAnIssueIsACreation(t *testing.T) {
	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)
	changes := subscribe(t, e.Hub)

	postEvents(t, e, key, eventWith(1, exc("TypeError", "x")))
	waitPending(t, e.Queue, 0)

	issue := listIssues(t, e, project.ID)[0]
	waitChange(t, changes, hub.Change{Kind: hub.IssueCreated, ProjectID: project.ID, IssueID: issue.ID})
}

func TestManualTransitionsAreRecorded(t *testing.T) {
	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)
	postEvents(t, e, key, eventWith(1, exc("TypeError", "x")))
	waitPending(t, e.Queue, 0)
	created := listIssues(t, e, project.ID)[0]
	if !created.StatusChangedAt.Equal(created.FirstSeen) || !created.ResolvedAt.IsZero() {
		t.Errorf("new issue: status_changed_at = %s, resolved_at = %s; want first_seen and none", created.StatusChangedAt, created.ResolvedAt)
	}

	setStatus(t, e, created.ID, issues.StatusResolved)
	resolved := listIssues(t, e, project.ID)[0]
	setStatus(t, e, created.ID, issues.StatusIgnored)
	ignored := listIssues(t, e, project.ID)[0]
	setStatus(t, e, created.ID, issues.StatusOpen)
	opened := listIssues(t, e, project.ID)[0]

	if resolved.Status != issues.StatusResolved || resolved.ResolvedAt.IsZero() || !resolved.StatusChangedAt.Equal(resolved.ResolvedAt) {
		t.Errorf("resolved: %s at %s, changed at %s; want resolved now", resolved.Status, resolved.ResolvedAt, resolved.StatusChangedAt)
	}
	if ignored.Status != issues.StatusIgnored || !ignored.StatusChangedAt.After(resolved.StatusChangedAt) || !ignored.ResolvedAt.Equal(resolved.ResolvedAt) {
		t.Errorf("ignored: %s, changed at %s, resolved at %s; want a later change that keeps resolved_at", ignored.Status, ignored.StatusChangedAt, ignored.ResolvedAt)
	}
	if opened.Status != issues.StatusOpen || !opened.StatusChangedAt.After(ignored.StatusChangedAt) || !opened.RegressedAt.IsZero() {
		t.Errorf("opened: %s, changed at %s, regressed at %s; want open by hand, not a Regression", opened.Status, opened.StatusChangedAt, opened.RegressedAt)
	}
}

func TestUnknownStatusIsRefused(t *testing.T) {
	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)
	postEvents(t, e, key, eventWith(1, exc("TypeError", "x")))
	waitPending(t, e.Queue, 0)
	issue := listIssues(t, e, project.ID)[0]

	if err := issues.SetStatus(t.Context(), e.Data, issue.ID, "archived", time.Now()); err == nil {
		t.Error("set status archived = nil, want an error")
	}
	if err := issues.SetStatus(t.Context(), e.Data, "0190b7a4-7c1e-7000-8000-000000000000", issues.StatusResolved, time.Now()); err == nil {
		t.Error("set status of an unknown Issue = nil, want an error")
	}
}

func TestEventOfADeletedProjectIsDropped(t *testing.T) {
	release := make(chan struct{})
	logs := &syncBuffer{}
	e := newEndpoint(t, withProcessAfter(release), withLogs(logs, 0))
	deleted, deletedKey := createProject(t, e)
	kept, keptKey := createProject(t, e)
	postEvents(t, e, deletedKey, eventWith(1, exc("TypeError", "x")))
	postEvents(t, e, keptKey, eventWith(2, exc("TypeError", "x")))

	if err := e.Projects.Delete(t.Context(), deleted.ID); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	close(release)
	waitPending(t, e.Queue, 0)

	if got := listIssues(t, e, kept.ID); len(got) != 1 {
		t.Errorf("issues of the kept Project = %d, want 1", len(got))
	}
	record := logs.find(t, "drop queued Event of a deleted Project")
	if record == nil || record["project_id"] != deleted.ID || record["event_id"] != eventID(1) {
		t.Errorf("log record = %v, want one with the Project and event_id", record)
	}
}

func setStatus(t *testing.T, e *endpoint, issueID string, status issues.Status) {
	t.Helper()

	// Each change gets a later time than the one before.
	time.Sleep(time.Millisecond)
	if err := issues.SetStatus(t.Context(), e.Data, issueID, status, time.Now()); err != nil {
		t.Fatalf("set status %s: %v", status, err)
	}
}

func subscribe(t *testing.T, h *hub.Hub) <-chan hub.Change {
	t.Helper()

	changes, unsubscribe := h.Subscribe()
	t.Cleanup(unsubscribe)
	return changes
}

// waitChange fails unless changes receives want.
func waitChange(t *testing.T, changes <-chan hub.Change, want hub.Change) {
	t.Helper()

	var got []hub.Change
	deadline := time.After(timeout)
	for {
		select {
		case change := <-changes:
			got = append(got, change)
			if slices.Contains(got, want) {
				return
			}
		case <-deadline:
			t.Fatalf("changes = %+v, want %+v", got, want)
		}
	}
}
