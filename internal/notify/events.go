package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Event is a notification type with its own on/off toggle.
type Event string

const (
	EpisodeReady    Event = "episode_ready"
	JobFailed       Event = "job_failed"
	UncertainPaid   Event = "uncertain_paid_outcome"
	BudgetThreshold Event = "budget_threshold"
	EmergencyStop   Event = "emergency_stop"
	VersionApproved Event = "version_approved" // covers approved and exported
	DailySummary    Event = "daily_summary"
	Test            Event = "test"
)

const (
	colorAmber = 0xF5A623
	colorRed   = 0xE5484D
	colorGreen = 0x30A46C
	colorBlue  = 0x3E63DD
)

// EventInfo describes one event for the settings API. Implemented is false where nothing in the
// codebase raises the event yet; the UI must say "Not implemented" for those.
type EventInfo struct {
	Event       Event
	Default     bool
	Color       int
	Implemented bool
}

// Events is the full list from notifications.md.
var Events = []EventInfo{
	{EpisodeReady, true, colorAmber, true},
	{JobFailed, true, colorRed, true},          // raised by the queue when a job becomes failed
	{UncertainPaid, true, colorRed, false},     // M2 provider budget work
	{BudgetThreshold, true, colorAmber, false}, // M2 budget work
	{EmergencyStop, true, colorRed, false},     // M2 kill switch
	{VersionApproved, false, colorGreen, true},
	{DailySummary, false, colorBlue, false}, // needs a scheduler
}

func lookup(e Event) (EventInfo, bool) {
	for _, i := range Events {
		if i.Event == e {
			return i, true
		}
	}
	return EventInfo{}, false
}

// Info is everything a message may contain: episode number and title, version, state, short reason.
type Info struct {
	EpisodeNumber int
	Title         string
	Version       int
	State         string
	Reason        string
}

var (
	urlRe  = regexp.MustCompile(`https?://\S+`)
	pathRe = regexp.MustCompile(`(?:[A-Za-z]:)?(?:/[\w.\-~]+){2,}/?`)
	wsRe   = regexp.MustCompile(`\s+`)
)

// clean keeps free text safe for a channel: no URLs (a webhook URL would be one), no file paths,
// first line only (so no stack traces), bounded length.
func clean(s string, max int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n") // first line only: a stack trace starts on line 2
	s = urlRe.ReplaceAllString(s, "[link]")
	s = pathRe.ReplaceAllString(s, "[path]")
	s = strings.TrimSpace(wsRe.ReplaceAllString(s, " "))
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max-1]) + "…"
	}
	return s
}

// BuildMessage renders text for an event. It is pure so every event's body can be tested.
func BuildMessage(e Event, in Info) Message {
	color := colorBlue
	if i, ok := lookup(e); ok {
		color = i.Color
	}
	ep := fmt.Sprintf("Episode %d — %s", in.EpisodeNumber, clean(in.Title, 80))
	var title string
	switch e {
	case EpisodeReady:
		title = "Episode ready for review"
	case JobFailed:
		title = "Job failed"
	case VersionApproved:
		title = "Version " + clean(in.State, 20)
	case Test:
		title = "TEST message from AI Content Studio"
		ep = "This is a test. No episode is involved and nothing needs doing."
	default:
		title = string(e)
	}
	var b strings.Builder
	b.WriteString(ep)
	if in.Version > 0 {
		fmt.Fprintf(&b, "\nVersion: v%d", in.Version)
	}
	if in.State != "" && e != VersionApproved {
		fmt.Fprintf(&b, "\nState: %s", clean(in.State, 40))
	}
	if in.Reason != "" {
		fmt.Fprintf(&b, "\nReason: %s", clean(in.Reason, 200))
	}
	return Message{Title: title, Description: b.String(), Color: color}
}

// DBTX is satisfied by both *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Enqueue records one notification in the outbox. It does nothing if the event is toggled off,
// and is idempotent on eventID. It never calls Discord, so it cannot block or fail a pipeline
// stage on Discord being down; callers log an error from it and carry on.
func Enqueue(ctx context.Context, db DBTX, e Event, eventID string, episodeID int64, version int, state, reason string) error {
	info, ok := lookup(e)
	if !ok || !info.Implemented {
		return fmt.Errorf("event %q cannot be enqueued", e)
	}
	var number int
	var title string
	if err := db.QueryRow(ctx, `SELECT number, title FROM episodes WHERE id = $1`, episodeID).Scan(&number, &title); err != nil {
		return fmt.Errorf("notification episode lookup: %w", err)
	}
	payload, err := json.Marshal(BuildMessage(e, Info{EpisodeNumber: number, Title: title, Version: version, State: state, Reason: reason}))
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO notifications (event_id, event, payload)
		SELECT $1, $2, $3 WHERE COALESCE((SELECT enabled FROM notification_settings WHERE event = $2), $4)
		ON CONFLICT (event_id) DO NOTHING`, eventID, string(e), payload, info.Default)
	if err != nil {
		return fmt.Errorf("enqueue notification: %w", err)
	}
	return nil
}
