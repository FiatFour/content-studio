package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/media"
	"github.com/fiat/content-studio/internal/providers"
)

var timingInfo = providers.Info{Provider: "studio", Model: "timeline-v1"}

// subtitlesStage measures the line audio, plans the timeline, and writes the dialogue track and SRT.
type subtitlesStage struct{ d *Deps }

func (s *subtitlesStage) Run(ctx context.Context, j *jobs.Job, progress func(int, int)) (func(pgx.Tx) error, error) {
	d := s.d
	e, err := d.episode(ctx, j.EpisodeID)
	if err != nil {
		return nil, err
	}
	script, scriptArt, err := d.loadScript(ctx, j)
	if err != nil {
		return nil, err
	}
	lines := script.Lines()
	var clips []artifacts.Artifact
	if len(lines) > 0 {
		if clips, err = d.inputs(ctx, j, artifacts.LineAudio); err != nil {
			return nil, err
		}
	}
	if len(clips) != len(lines) {
		return nil, jobs.Errorf(jobs.ErrInvalidInput, nil, "script has %d lines but %d audio clips exist", len(lines), len(clips))
	}
	pcms := make([]media.PCM, len(clips))
	durs := make([]time.Duration, len(clips))
	rate := 48000
	for i, c := range clips {
		data, err := d.read(c)
		if err != nil {
			return nil, err
		}
		if pcms[i], err = media.ParseWAV(data); err != nil {
			return nil, jobs.Errorf(jobs.ErrInvalidInput, err, "line %d audio is unreadable", i+1)
		}
		if i == 0 {
			rate = pcms[i].SampleRate
		} else if pcms[i].SampleRate != rate {
			return nil, jobs.Errorf(jobs.ErrInvalidInput, nil, "line %d audio is %d Hz, others are %d Hz", i+1, pcms[i].SampleRate, rate)
		}
		durs[i] = pcms[i].Duration()
	}
	tl, err := PlanTimeline(script.Shots(), durs, e.TargetDurationS)
	if err != nil {
		return nil, jobs.Errorf(jobs.ErrInvalidInput, err, "%s", err.Error())
	}

	// Dialogue track: silence with each clip placed at its planned start, sample-exact.
	total := int(tl.TotalMS * int64(rate) / 1000)
	track := make([]int16, total)
	for i, l := range tl.Lines {
		at := int(l.StartMS * int64(rate) / 1000)
		copy(track[at:], pcms[i].Samples)
	}
	var wav bytes.Buffer
	if err := media.WriteWAV(&wav, media.PCM{SampleRate: rate, Samples: track}); err != nil {
		return nil, err
	}

	var cues []Cue
	for i, l := range tl.Lines {
		progress(i, len(tl.Lines))
		words, err := d.Segmenter.Segment(ctx, l.Text)
		if err != nil {
			return nil, err
		}
		cues = append(cues, Cues(l, words)...)
	}

	out := d.newOutputs(j)
	parents := append([]int64{scriptArt.ID}, ids(clips)...)
	tlJSON, err := json.MarshalIndent(tl, "", "  ")
	if err != nil {
		return nil, err
	}
	tla, err := out.bytes(artifacts.Timeline, "", tlJSON, timingInfo, map[string]any{"total_ms": tl.TotalMS, "target_s": e.TargetDurationS}, parents)
	if err != nil {
		return nil, err
	}
	if _, err := out.bytes(artifacts.Dialogue, "", wav.Bytes(), timingInfo, map[string]any{"duration_ms": tl.TotalMS, "sample_rate": rate}, parents, tla); err != nil {
		return nil, err
	}
	if _, err := out.bytes(artifacts.Subtitles, "", []byte(WriteSRT(cues)), timingInfo, map[string]any{"cues": len(cues), "language": "th"}, parents, tla); err != nil {
		return nil, err
	}
	return out.persist(nil), nil
}
