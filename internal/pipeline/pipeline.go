// Package pipeline implements the Fictional Series stages: script, tts, images, subtitles, render, qa.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/media"
	"github.com/fiat/content-studio/internal/providers"
)

// FictionalSeries is the M1a profile: plain data, as product.md asks until a second profile exists.
var FictionalSeries = jobs.Plan{
	Profile:  "fictional_series",
	Full:     []string{"script", "tts", "images", "subtitles", "render", "qa"},
	Rerender: []string{"render", "qa"},
}

type Settings struct {
	ImageWidth, ImageHeight int
	OutWidth, OutHeight     int
	FPS                     int
	SubtitleFont            string
	ScriptRetries           int
	ExportDir               string
}

type Deps struct {
	DB        *pgxpool.Pool
	Store     *artifacts.Store
	Queue     *jobs.Queue
	LLM       providers.LLM
	Images    providers.ImageGen
	Voice     providers.Voice
	Segmenter providers.Segmenter
	GPU       *providers.GPU
	Tools     media.Tools
	Settings  Settings
	Log       *slog.Logger
}

// Stages wires every stage of the profile to its implementation.
func (d *Deps) Stages() map[string]jobs.Stage {
	return map[string]jobs.Stage{
		"script":    &scriptStage{d},
		"images":    &imagesStage{d},
		"tts":       &ttsStage{d},
		"subtitles": &subtitlesStage{d},
		"render":    &renderStage{d},
		"qa":        &qaStage{d},
	}
}

type Episode struct {
	ID, ProjectID   int64
	Number          int
	Title, Idea     string
	TargetDurationS int
}

// runFeedback is the note an automatic rewrite left for the scriptwriter; empty for an ordinary run.
func (d *Deps) runFeedback(ctx context.Context, runID int64) (string, error) {
	var f string
	err := d.DB.QueryRow(ctx, `SELECT coalesce(feedback, '') FROM workflow_runs WHERE id = $1`, runID).Scan(&f)
	return f, err
}

func (d *Deps) episode(ctx context.Context, id int64) (Episode, error) {
	var e Episode
	err := d.DB.QueryRow(ctx, `SELECT id, project_id, number, title, idea, target_duration_s FROM episodes WHERE id = $1`, id).
		Scan(&e.ID, &e.ProjectID, &e.Number, &e.Title, &e.Idea, &e.TargetDurationS)
	return e, err
}

// inputRun is the run whose script, images, audio and subtitles this job uses. A re-render
// reuses the outputs of the full run it was started from.
func (d *Deps) inputRun(ctx context.Context, j *jobs.Job) (int64, error) {
	runID, kind, src := j.RunID, j.RunKind, j.ParentRunID
	for kind == "rerender" {
		if src == nil {
			return 0, fmt.Errorf("rerender run %d has no source run", runID)
		}
		runID = *src
		if err := d.DB.QueryRow(ctx, `SELECT kind, parent_run_id FROM workflow_runs WHERE id = $1`, runID).Scan(&kind, &src); err != nil {
			return 0, err
		}
	}
	return runID, nil
}

func (d *Deps) inputs(ctx context.Context, j *jobs.Job, typ artifacts.Type) ([]artifacts.Artifact, error) {
	runID, err := d.inputRun(ctx, j)
	if err != nil {
		return nil, err
	}
	list, err := artifacts.ForRun(ctx, d.DB, runID, typ)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, jobs.Errorf(jobs.ErrInvalidInput, nil, "no %s artifacts from run %d", typ, runID)
	}
	return list, nil
}

func (d *Deps) loadScript(ctx context.Context, j *jobs.Job) (*Script, artifacts.Artifact, error) {
	list, err := d.inputs(ctx, j, artifacts.Script)
	if err != nil {
		return nil, artifacts.Artifact{}, err
	}
	a := list[len(list)-1]
	data, err := d.read(a)
	if err != nil {
		return nil, a, err
	}
	var s Script
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, a, err
	}
	return &s, a, nil
}

// read loads an artifact's bytes and refuses them if the file no longer matches its recorded hash.
func (d *Deps) read(a artifacts.Artifact) ([]byte, error) {
	path, err := d.Store.Abs(a.StoragePath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, jobs.Errorf(jobs.ErrInvalidInput, err, "%s artifact %d is missing on disk", a.Type, a.ID)
	}
	if got := sha256Hex(data); got != a.SHA256 {
		return nil, jobs.Errorf(jobs.ErrInvalidInput, nil, "%s artifact %d does not match its recorded hash", a.Type, a.ID)
	}
	return data, nil
}

// outputs collects a stage's files and records them in the job-completing transaction.
type outputs struct {
	d     *Deps
	j     *jobs.Job
	items []pending
}

type pending struct {
	a       *artifacts.Artifact
	parents []*artifacts.Artifact
}

func (d *Deps) newOutputs(j *jobs.Job) *outputs { return &outputs{d: d, j: j} }

func (o *outputs) loc() artifacts.Loc {
	return artifacts.Loc{EpisodeID: o.j.EpisodeID, RunID: o.j.RunID, JobID: o.j.ID, Attempt: o.j.Attempt}
}

func (o *outputs) add(f artifacts.File, typ artifacts.Type, key string, info providers.Info, meta map[string]any, parentIDs []int64, parents ...*artifacts.Artifact) *artifacts.Artifact {
	a := &artifacts.Artifact{
		ProjectID: o.j.ProjectID, EpisodeID: o.j.EpisodeID, RunID: o.j.RunID, JobID: o.j.ID,
		Type: typ, ItemKey: key, StoragePath: f.RelPath, SHA256: f.SHA256, SizeBytes: f.Size,
		Provider: info.Provider, Model: info.Model, Meta: meta, Parents: parentIDs,
	}
	o.items = append(o.items, pending{a: a, parents: parents})
	return a
}

func (o *outputs) bytes(typ artifacts.Type, key string, data []byte, info providers.Info, meta map[string]any, parentIDs []int64, parents ...*artifacts.Artifact) (*artifacts.Artifact, error) {
	f, err := o.d.Store.Write(o.loc(), typ, key, func(w io.Writer) error { _, err := w.Write(data); return err })
	if err != nil {
		return nil, err
	}
	return o.add(f, typ, key, info, meta, parentIDs, parents...), nil
}

func (o *outputs) file(typ artifacts.Type, key, src string, info providers.Info, meta map[string]any, parentIDs []int64) (*artifacts.Artifact, error) {
	f, err := o.d.Store.WriteFile(o.loc(), typ, key, src)
	if err != nil {
		return nil, err
	}
	return o.add(f, typ, key, info, meta, parentIDs), nil
}

func (o *outputs) persist(extra func(pgx.Tx) error) func(pgx.Tx) error {
	return func(tx pgx.Tx) error {
		ctx := context.Background()
		for _, p := range o.items {
			for _, parent := range p.parents {
				p.a.Parents = append(p.a.Parents, parent.ID)
			}
			if err := artifacts.Insert(ctx, tx, p.a); err != nil {
				return err
			}
		}
		if extra != nil {
			return extra(tx)
		}
		return nil
	}
}

func ids(list []artifacts.Artifact) []int64 {
	out := make([]int64, len(list))
	for i, a := range list {
		out[i] = a.ID
	}
	return out
}

// workDir is a scratch directory for one job attempt, removed when the stage ends.
func (d *Deps) workDir(j *jobs.Job) (string, func(), error) {
	dir, err := os.MkdirTemp("", fmt.Sprintf("studio-job%d-a%d-", j.ID, j.Attempt))
	if err != nil {
		return "", nil, err
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}
