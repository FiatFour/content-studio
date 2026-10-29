package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/canon"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/providers"
)

// Every stage implements jobs.Keyed. The hash covers what the stage reads: the episode, the
// settings and models it runs with, and the content hash of every input artifact (never ids or
// paths, so identical inputs hash the same). The worker combines it with run ID and stage into the
// idempotency key.

// inputHash hashes extra (JSON-encoded) and the stored inputs of the given types from the run whose
// outputs this job reads.
func (d *Deps) inputHash(ctx context.Context, srcRun int64, extra any, types ...artifacts.Type) (string, error) {
	h := sha256.New()
	enc, err := json.Marshal(extra)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "extra:%s\n", enc)
	for _, typ := range types {
		list, err := artifacts.ForRun(ctx, d.DB, srcRun, typ)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "type:%s n:%d\n", typ, len(list))
		for _, a := range list {
			fmt.Fprintf(h, "%s|%s|%s\n", a.Type, a.ItemKey, a.SHA256)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// downstreamHash is inputHash for stages that read the outputs of the run they belong to or, for a
// re-render, of the full run it was started from.
func (d *Deps) downstreamHash(ctx context.Context, j *jobs.Job, extra any, types ...artifacts.Type) (string, error) {
	src, err := d.inputRun(ctx, j)
	if err != nil {
		return "", err
	}
	return d.inputHash(ctx, src, extra, types...)
}

func (s *scriptStage) InputHash(ctx context.Context, j *jobs.Job) (string, error) {
	d := s.d
	e, err := d.episode(ctx, j.EpisodeID)
	if err != nil {
		return "", err
	}
	feedback, err := d.runFeedback(ctx, j.RunID)
	if err != nil {
		return "", err
	}
	chars, err := canon.Characters(ctx, d.DB, e.ProjectID)
	if err != nil {
		return "", err
	}
	var sums any
	if len(chars) > 0 {
		if sums, err = canon.RecentSummaries(ctx, d.DB, e.ProjectID, e.Number, recentSummaryCount); err != nil {
			return "", err
		}
	}
	llm, err := resolve[providers.LLM](d, providers.TaskScript)
	if err != nil {
		return "", err
	}
	return d.inputHash(ctx, j.RunID, map[string]any{"episode": e, "feedback": feedback, "llm": llm.Info(), "characters": chars,
		"summaries": sums, "retries": d.Settings.ScriptRetries, "prompt": []int{scriptPromptVersion, scriptPromptVersionCanon}})
}

func (s *ttsStage) InputHash(ctx context.Context, j *jobs.Job) (string, error) {
	voice, err := resolve[providers.Voice](s.d, providers.TaskTTS)
	if err != nil {
		return "", err
	}
	return s.d.downstreamHash(ctx, j, map[string]any{"voice": voice.Info()}, artifacts.Script)
}

func (s *imagesStage) InputHash(ctx context.Context, j *jobs.Job) (string, error) {
	images, err := resolve[providers.ImageGen](s.d, providers.TaskImages)
	if err != nil {
		return "", err
	}
	st := s.d.Settings
	return s.d.downstreamHash(ctx, j, map[string]any{"images": images.Info(), "w": st.ImageWidth, "h": st.ImageHeight,
		"lora": st.LoraStrength, "style": styleVersion, "episode": j.EpisodeID}, artifacts.Script)
}

func (s *subtitlesStage) InputHash(ctx context.Context, j *jobs.Job) (string, error) {
	e, err := s.d.episode(ctx, j.EpisodeID)
	if err != nil {
		return "", err
	}
	return s.d.downstreamHash(ctx, j, map[string]any{"episode": e, "font": s.d.Settings.SubtitleFont}, artifacts.Script, artifacts.LineAudio)
}

func (s *renderStage) InputHash(ctx context.Context, j *jobs.Job) (string, error) {
	return s.d.downstreamHash(ctx, j, map[string]any{"settings": s.d.Settings, "episode": j.EpisodeID},
		artifacts.Timeline, artifacts.Image, artifacts.Dialogue, artifacts.Subtitles)
}

// qaStage checks the video of its own run.
func (s *qaStage) InputHash(ctx context.Context, j *jobs.Job) (string, error) {
	e, err := s.d.episode(ctx, j.EpisodeID)
	if err != nil {
		return "", err
	}
	return s.d.inputHash(ctx, j.RunID, map[string]any{"episode": e}, artifacts.Video, artifacts.RenderSpec)
}
