// Package issues applies processed Events to the Issues that group them.
package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/joansgarcia/trace/event"
	"github.com/joansgarcia/trace/grouping"
	"github.com/joansgarcia/trace/store"
)

// Status is the lifecycle state of an Issue.
type Status string

const (
	StatusOpen     Status = "open"
	StatusResolved Status = "resolved"
	StatusIgnored  Status = "ignored"
)

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
	Culprit    string
	EventCount int
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
// adds it to its Issue, creating the Issue if it is the first of its Group
// Key. It does it all in one transaction, and does nothing for an event_id
// the Project already stored, so recording an Event again is safe. It
// reports whether it stored event.
func Record(ctx context.Context, data *store.DB, projectID string, receivedAt time.Time, event event.Event) (bool, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return false, fmt.Errorf("encode Event: %w", err)
	}

	tx, err := data.Write.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin record Event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var stored int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE project_id = ? AND event_id = ?", projectID, event.EventID).Scan(&stored); err != nil {
		return false, fmt.Errorf("find Event: %w", err)
	}
	if stored > 0 {
		return false, nil
	}

	group := grouping.Compute(event)
	components, err := json.Marshal(group.Components)
	if err != nil {
		return false, fmt.Errorf("encode group components: %w", err)
	}

	// On conflict, RETURNING gives the id of the existing Issue.
	var issueID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO issues (id, project_id, group_key, status, title, culprit, event_count)
		VALUES (?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT (project_id, group_key) DO UPDATE SET event_count = event_count + 1
		RETURNING id`,
		store.NewID(), projectID, group.Key, StatusOpen, title(event), culprit(event),
	).Scan(&issueID)
	if err != nil {
		return false, fmt.Errorf("upsert Issue: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (id, project_id, event_id, issue_id, received_at, grouping_version, group_components, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		store.NewID(), projectID, event.EventID, issueID, receivedAt.UTC().Format(time.RFC3339Nano), group.Version, string(components), payload)
	if err != nil {
		return false, fmt.Errorf("insert Event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit record Event: %w", err)
	}
	return true, nil
}

// List returns the Issues of the Project projectID, oldest first.
func List(ctx context.Context, data *store.DB, projectID string) ([]Issue, error) {
	// rowid follows the order of insertion, which ids within one
	// millisecond do not.
	rows, err := data.Read.QueryContext(ctx, "SELECT id, project_id, status, title, culprit, event_count FROM issues WHERE project_id = ? ORDER BY rowid", projectID)
	if err != nil {
		return nil, fmt.Errorf("list Issues: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var issues []Issue
	for rows.Next() {
		var issue Issue
		if err := rows.Scan(&issue.ID, &issue.ProjectID, &issue.Status, &issue.Title, &issue.Culprit, &issue.EventCount); err != nil {
			return nil, fmt.Errorf("list Issues: %w", err)
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
		if e.ReceivedAt, err = time.Parse(time.RFC3339Nano, receivedAt); err != nil {
			return nil, fmt.Errorf("decode received_at: %w", err)
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
