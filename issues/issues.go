// Package issues applies processed Events to the Issues that group them and
// carries the lifecycle of each Issue.
package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/joansgarcia/trace/event"
	"github.com/joansgarcia/trace/grouping"
	"github.com/joansgarcia/trace/store"
)

// timeLayout stores times in UTC with a fixed width, so they sort as text.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// The lifecycle states of an Issue.
const (
	StatusOpen     Status = "open"
	StatusResolved Status = "resolved"
	StatusIgnored  Status = "ignored"
)

// The kinds of change Record makes to an Issue.
const (
	ChangeCreated   ChangeKind = "created"
	ChangeUpdated   ChangeKind = "updated"
	ChangeRegressed ChangeKind = "regressed"
)

var (
	// ErrProjectNotFound is returned by Record for an Event whose Project
	// no longer exists.
	ErrProjectNotFound = errors.New("project not found")
	// ErrNotFound is returned by SetStatus for an Issue that does not exist.
	ErrNotFound = errors.New("issue not found")
	// ErrInvalidStatus is returned by SetStatus for an unknown status.
	ErrInvalidStatus = errors.New("invalid issue status")
)

// severity orders the Levels from the least to the most severe.
var severity = []event.Level{event.LevelWarning, event.LevelError, event.LevelFatal}

// Status is the lifecycle state of an Issue. It is global to its Project
// and Group Key, across Environments and Releases.
type Status string

// ChangeKind is what an Event did to its Issue.
type ChangeKind string

// Change is the change Record made to an Issue.
type Change struct {
	Kind    ChangeKind
	IssueID string
}

// Issue is a group of equivalent Events of a Project.
type Issue struct {
	// ID is a UUIDv7.
	ID        string
	ProjectID string
	Status    Status
	// Title is "type: message" of the first exception of the first Event.
	Title string
	// Culprit is "function @ filename" of the first In-app frame of the
	// first Event, or empty if it has none.
	Culprit string
	// Level is the most severe Level of its Events.
	Level      event.Level
	EventCount int
	// UserCount is the number of distinct Users of its Events. Events
	// without a User do not count.
	UserCount int
	// FirstSeen and LastSeen are when the server received its first and
	// last Event.
	FirstSeen time.Time
	LastSeen  time.Time
	// StatusChangedAt is the time of the last change of Status, by hand or
	// by a Regression.
	StatusChangedAt time.Time
	// ResolvedAt is the time of the last resolution, and RegressedAt of the
	// last Regression; zero if none.
	ResolvedAt  time.Time
	RegressedAt time.Time
}

// Event is one processed Event as stored.
type Event struct {
	// ID is a UUIDv7.
	ID         string
	IssueID    string
	ReceivedAt time.Time
	// GroupingVersion is the version of the Group Key algorithm that
	// grouped the Event, and GroupComponents the readable components of its
	// Group Key: why it belongs to its Issue.
	GroupingVersion int
	GroupComponents []string
	Event           event.Event
}

// Record stores event, received at receivedAt for the Project projectID, and
// applies it to its Issue in one transaction: it creates the Issue for the
// first Event of a Group Key, updates its aggregates, and reopens it if it
// was resolved, which is a Regression. An ignored Issue stays ignored.
//
// Record does nothing for an event_id the Project already stored, so
// recording an Event again is safe; it then returns a zero Change. It fails
// with ErrProjectNotFound if the Project no longer exists.
func Record(ctx context.Context, data *store.DB, projectID string, receivedAt time.Time, event event.Event) (Change, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return Change{}, fmt.Errorf("encode Event: %w", err)
	}
	group := grouping.Compute(event)
	components, err := json.Marshal(group.Components)
	if err != nil {
		return Change{}, fmt.Errorf("encode group components: %w", err)
	}
	now := receivedAt.UTC().Format(timeLayout)

	tx, err := data.Write.BeginTx(ctx, nil)
	if err != nil {
		return Change{}, fmt.Errorf("begin record Event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var projects, stored int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM projects WHERE id = ?", projectID).Scan(&projects); err != nil {
		return Change{}, fmt.Errorf("find Project: %w", err)
	}
	if projects == 0 {
		return Change{}, ErrProjectNotFound
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE project_id = ? AND event_id = ?", projectID, event.EventID).Scan(&stored); err != nil {
		return Change{}, fmt.Errorf("find Event: %w", err)
	}
	if stored > 0 {
		return Change{}, nil
	}

	change, err := applyEvent(ctx, tx, projectID, group.Key, now, event)
	if err != nil {
		return Change{}, err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (id, project_id, event_id, issue_id, received_at, grouping_version, group_components, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		store.NewID(), projectID, event.EventID, change.IssueID, now, group.Version, string(components), payload)
	if err != nil {
		return Change{}, fmt.Errorf("insert Event: %w", err)
	}

	if event.User != nil {
		if err := countUser(ctx, tx, change.IssueID, event.User.ID); err != nil {
			return Change{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return Change{}, fmt.Errorf("commit record Event: %w", err)
	}
	return change, nil
}

// SetStatus changes the status of the Issue issueID by hand, at now. Every
// transition between the three states is allowed; setting the current
// status changes nothing. A resolution also records resolved_at.
func SetStatus(ctx context.Context, data *store.DB, issueID string, status Status, now time.Time) error {
	if !slices.Contains([]Status{StatusOpen, StatusResolved, StatusIgnored}, status) {
		return fmt.Errorf("%w: %q", ErrInvalidStatus, status)
	}
	at := now.UTC().Format(timeLayout)
	result, err := data.Write.ExecContext(ctx, `
		UPDATE issues SET
			status = ?1,
			status_changed_at = ?2,
			resolved_at = CASE WHEN ?1 = 'resolved' THEN ?2 ELSE resolved_at END
		WHERE id = ?3 AND status != ?1`,
		status, at, issueID)
	if err != nil {
		return fmt.Errorf("set issue status: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("set issue status: %w", err)
	} else if n > 0 {
		return nil
	}

	var exists int
	if err := data.Write.QueryRowContext(ctx, "SELECT count(*) FROM issues WHERE id = ?", issueID).Scan(&exists); err != nil {
		return fmt.Errorf("find issue: %w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns the Issues of the Project projectID, oldest first.
func List(ctx context.Context, data *store.DB, projectID string) ([]Issue, error) {
	// rowid follows the order of insertion, which ids within one
	// millisecond do not.
	rows, err := data.Read.QueryContext(ctx, `
		SELECT id, project_id, status, title, culprit, level, event_count, user_count,
			first_seen, last_seen, status_changed_at, coalesce(resolved_at, ''), coalesce(regressed_at, '')
		FROM issues WHERE project_id = ? ORDER BY rowid`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list Issues: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var issues []Issue
	for rows.Next() {
		var issue Issue
		var firstSeen, lastSeen, changedAt, resolvedAt, regressedAt string
		if err := rows.Scan(&issue.ID, &issue.ProjectID, &issue.Status, &issue.Title, &issue.Culprit, &issue.Level, &issue.EventCount, &issue.UserCount,
			&firstSeen, &lastSeen, &changedAt, &resolvedAt, &regressedAt); err != nil {
			return nil, fmt.Errorf("list Issues: %w", err)
		}
		times := []struct {
			text string
			to   *time.Time
		}{
			{firstSeen, &issue.FirstSeen}, {lastSeen, &issue.LastSeen}, {changedAt, &issue.StatusChangedAt},
			{resolvedAt, &issue.ResolvedAt}, {regressedAt, &issue.RegressedAt},
		}
		for _, t := range times {
			if *t.to, err = parseTime(t.text); err != nil {
				return nil, fmt.Errorf("list Issues: %w", err)
			}
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

// ListEvents returns the Events of the Project projectID, oldest first.
func ListEvents(ctx context.Context, data *store.DB, projectID string) ([]Event, error) {
	rows, err := data.Read.QueryContext(ctx, "SELECT id, issue_id, received_at, grouping_version, group_components, payload FROM events WHERE project_id = ? ORDER BY rowid", projectID)
	if err != nil {
		return nil, fmt.Errorf("list Events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []Event
	for rows.Next() {
		var e Event
		var receivedAt, components string
		var payload []byte
		if err := rows.Scan(&e.ID, &e.IssueID, &receivedAt, &e.GroupingVersion, &components, &payload); err != nil {
			return nil, fmt.Errorf("list Events: %w", err)
		}
		if e.ReceivedAt, err = parseTime(receivedAt); err != nil {
			return nil, fmt.Errorf("list Events: %w", err)
		}
		if err := json.Unmarshal([]byte(components), &e.GroupComponents); err != nil {
			return nil, fmt.Errorf("decode group components: %w", err)
		}
		if err := json.Unmarshal(payload, &e.Event); err != nil {
			return nil, fmt.Errorf("decode Event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// applyEvent creates or updates the Issue of groupKey for e, received at
// now, and returns what it did.
func applyEvent(ctx context.Context, tx *sql.Tx, projectID, groupKey, now string, e event.Event) (Change, error) {
	var issueID, firstSeen, lastSeen string
	var status Status
	var level event.Level
	err := tx.QueryRowContext(ctx, "SELECT id, status, level, first_seen, last_seen FROM issues WHERE project_id = ? AND group_key = ?", projectID, groupKey).
		Scan(&issueID, &status, &level, &firstSeen, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		issueID = store.NewID()
		_, err := tx.ExecContext(ctx, `
			INSERT INTO issues (id, project_id, group_key, status, title, culprit, level, event_count, user_count, first_seen, last_seen, status_changed_at)
			VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, 1, 0, ?8, ?8, ?8)`,
			issueID, projectID, groupKey, StatusOpen, title(e), culprit(e), e.Level, now)
		if err != nil {
			return Change{}, fmt.Errorf("insert Issue: %w", err)
		}
		return Change{Kind: ChangeCreated, IssueID: issueID}, nil
	}
	if err != nil {
		return Change{}, fmt.Errorf("find Issue: %w", err)
	}

	// Only a resolved Issue reopens: an ignored one stays ignored.
	kind := ChangeUpdated
	if status == StatusResolved {
		kind = ChangeRegressed
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE issues SET
			event_count = event_count + 1,
			level = ?1,
			first_seen = ?2,
			last_seen = ?3,
			status = CASE WHEN ?5 THEN 'open' ELSE status END,
			status_changed_at = CASE WHEN ?5 THEN ?6 ELSE status_changed_at END,
			regressed_at = CASE WHEN ?5 THEN ?6 ELSE regressed_at END
		WHERE id = ?4`,
		maxLevel(level, e.Level), min(firstSeen, now), max(lastSeen, now), issueID, kind == ChangeRegressed, now)
	if err != nil {
		return Change{}, fmt.Errorf("update Issue: %w", err)
	}
	return Change{Kind: kind, IssueID: issueID}, nil
}

// countUser adds userID to the Users of the Issue issueID, once.
func countUser(ctx context.Context, tx *sql.Tx, issueID, userID string) error {
	result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO issue_users (issue_id, user_id) VALUES (?, ?)", issueID, userID)
	if err != nil {
		return fmt.Errorf("insert Issue User: %w", err)
	}
	added, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("insert Issue User: %w", err)
	}
	if added == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE issues SET user_count = user_count + 1 WHERE id = ?", issueID); err != nil {
		return fmt.Errorf("count Issue User: %w", err)
	}
	return nil
}

// maxLevel returns the more severe of a and b.
func maxLevel(a, b event.Level) event.Level {
	if slices.Index(severity, b) > slices.Index(severity, a) {
		return b
	}
	return a
}

// parseTime parses a stored time; empty is the zero time.
func parseTime(text string) (time.Time, error) {
	if text == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, text)
}

func title(event event.Event) string {
	exception := event.Exceptions[0]
	if exception.Message == "" {
		return exception.Type
	}
	return exception.Type + ": " + exception.Message
}

func culprit(event event.Event) string {
	for _, exception := range event.Exceptions {
		for _, frame := range exception.Frames {
			if !frame.IsInApp() {
				continue
			}
			if frame.Function == "" {
				return frame.Filename
			}
			return frame.Function + " @ " + frame.Filename
		}
	}
	return ""
}
