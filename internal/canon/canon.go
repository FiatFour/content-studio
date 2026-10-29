// Package canon holds the M1b story bible: characters and episode summaries, the deterministic
// presence check, and the bounded context a model call is allowed to see (canon.md). Nothing here
// writes canon on behalf of a model; rows are changed only by the owner through the API.
package canon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	StatusAlive     = "alive"
	StatusDeceased  = "deceased"
	StatusMovedAway = "moved_away"
)

type Character struct {
	ID, ProjectID int64
	Slug, Name    string
	Appearance    string
	Personality   string
	Voice         string
	ArrivesOn     *time.Time
	Status        string
	StatusSince   *time.Time
	LoraName      string
	TriggerToken  string
}

type Summary struct {
	EpisodeID, Number int64
	Title             string
	Summary           string
	CharactersPresent []string
	OpenThreads       []string
}

const characterCols = `id, project_id, slug, name, appearance, personality, voice, arrives_on, status, status_since, lora_name, trigger_token`

// Characters returns every character of the project, ordered by slug.
func Characters(ctx context.Context, db *pgxpool.Pool, projectID int64) ([]Character, error) {
	rows, err := db.Query(ctx, `SELECT `+characterCols+` FROM characters WHERE project_id = $1 ORDER BY slug`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Character
	for rows.Next() {
		var c Character
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Slug, &c.Name, &c.Appearance, &c.Personality, &c.Voice,
			&c.ArrivesOn, &c.Status, &c.StatusSince, &c.LoraName, &c.TriggerToken); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecentSummaries returns up to n summaries of earlier episodes (lower number) of the project,
// oldest first, so the prompt reads in story order.
func RecentSummaries(ctx context.Context, db *pgxpool.Pool, projectID int64, beforeNumber, n int) ([]Summary, error) {
	rows, err := db.Query(ctx, `
		SELECT e.id, e.number, e.title, s.summary, s.characters_present, s.open_threads
		FROM episode_summaries s JOIN episodes e ON e.id = s.episode_id
		WHERE e.project_id = $1 AND e.number < $2
		ORDER BY e.number DESC LIMIT $3`, projectID, beforeNumber, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.EpisodeID, &s.Number, &s.Title, &s.Summary, &s.CharactersPresent, &s.OpenThreads); err != nil {
			return nil, err
		}
		out = append([]Summary{s}, out...)
	}
	return out, rows.Err()
}

// Present reports why c cannot appear at the in-story date at (nil when the episode has no date).
// A character is present when it has arrived and is alive and living here on that date. A nil date
// is judged by status alone: only an alive character is present.
func Present(c Character, at *time.Time) error {
	if at != nil && c.ArrivesOn != nil && at.Before(*c.ArrivesOn) {
		return fmt.Errorf("%s (%s) has not arrived yet on %s", c.Name, c.Slug, at.Format(time.DateOnly))
	}
	if c.Status == StatusAlive {
		return nil
	}
	if at == nil || c.StatusSince == nil || !at.Before(*c.StatusSince) {
		return fmt.Errorf("%s (%s) is %s and cannot appear", c.Name, c.Slug, strings.ReplaceAll(c.Status, "_", " "))
	}
	return nil
}

// CheckSpeakers verifies every speaker slug is a known character that is present at the date.
// The message is safe to show and to feed back to the writer model.
func CheckSpeakers(speakers []string, chars []Character, at *time.Time) error {
	bySlug := make(map[string]Character, len(chars))
	for _, c := range chars {
		bySlug[c.Slug] = c
	}
	seen := map[string]bool{}
	for _, sp := range speakers {
		if seen[sp] {
			continue
		}
		seen[sp] = true
		c, ok := bySlug[sp]
		if !ok {
			return fmt.Errorf("speaker %q is not a character of this series", sp)
		}
		if err := Present(c, at); err != nil {
			return err
		}
	}
	return nil
}

// PromptBlock renders the bounded context for the script prompt: the characters who may appear and
// the last summaries. It is deterministic and never includes more than the arguments hold.
func PromptBlock(chars []Character, summaries []Summary) string {
	var b strings.Builder
	b.WriteString("Series characters (use only these ids as characters and speakers):\n")
	for _, c := range chars {
		fmt.Fprintf(&b, "- id %q, %s. Looks: %s. Voice: %s.", c.Slug, c.Name, c.Appearance, c.Voice)
		if c.Personality != "" {
			fmt.Fprintf(&b, " Personality: %s.", c.Personality)
		}
		b.WriteString("\n")
	}
	if len(summaries) > 0 {
		b.WriteString("Recent episodes, oldest first:\n")
		for _, s := range summaries {
			fmt.Fprintf(&b, "- Episode %d %q: %s", s.Number, s.Title, s.Summary)
			if len(s.OpenThreads) > 0 {
				fmt.Fprintf(&b, " Open threads: %s.", strings.Join(s.OpenThreads, "; "))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
