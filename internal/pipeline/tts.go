package pipeline

import (
	"context"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/media"
	"github.com/fiat/content-studio/internal/providers"
)

// referenceText is what each character says once to create their voice. Every later line is
// cloned from that clip, so a character keeps one voice for the whole episode.
const referenceText = "สวัสดี วันนี้อากาศดีมาก เรามาเริ่มกันเลย"

// A clip faster or slower than these rates is treated as a failed generation (cut off or rambling).
const (
	minClipCharsPerSecond = 3.0
	maxClipCharsPerSecond = 30.0
)

type ttsStage struct{ d *Deps }

func (s *ttsStage) Run(ctx context.Context, j *jobs.Job, progress func(int, int)) (func(pgx.Tx) error, error) {
	d := s.d
	script, scriptArt, err := d.loadScript(ctx, j)
	if err != nil {
		return nil, err
	}
	voice, err := resolve[providers.Voice](d, providers.TaskTTS)
	if err != nil {
		return nil, err
	}
	if err := d.GPU.Acquire(ctx, gpuHolder(voice)); err != nil {
		return nil, err
	}
	info := voice.Info()
	if err := d.Queue.SetProvider(ctx, j.ID, info.Provider, info.Model); err != nil {
		return nil, err
	}
	lines := script.Lines()
	out := d.newOutputs(j)
	refs := map[string][]byte{}
	refArts := map[string]*artifacts.Artifact{}
	total := len(lines)
	durs := make([]time.Duration, 0, total)
	chars := 0
	for i, l := range lines {
		progress(i, total)
		if _, ok := refs[l.Speaker]; !ok {
			c, _ := script.Character(l.Speaker)
			wav, err := voice.Design(ctx, c.Voice, referenceText, int(j.EpisodeID))
			if err != nil {
				return nil, err
			}
			if _, err := checkClip(wav, referenceText, "voice reference for "+c.ID); err != nil {
				return nil, err
			}
			a, err := out.bytes(artifacts.VoiceRef, c.ID, wav, info, map[string]any{"character": c.ID, "description": c.Voice, "text": referenceText}, []int64{scriptArt.ID})
			if err != nil {
				return nil, err
			}
			refs[l.Speaker], refArts[l.Speaker] = wav, a
		}
		wav, err := voice.Clone(ctx, refs[l.Speaker], l.Text, int(j.EpisodeID)*1000+i)
		if err != nil {
			return nil, err
		}
		pcm, err := checkClip(wav, l.Text, "line "+strconv.Itoa(i+1))
		if err != nil {
			return nil, err
		}
		durs = append(durs, pcm.Duration())
		chars += utf8.RuneCountInString(l.Text)
		meta := map[string]any{"speaker": l.Speaker, "text": l.Text, "shot": l.Shot, "duration_ms": pcm.Duration().Milliseconds(), "sample_rate": pcm.SampleRate}
		if _, err := out.bytes(artifacts.LineAudio, lineKey(i), wav, info, meta, []int64{scriptArt.ID}, refArts[l.Speaker]); err != nil {
			return nil, err
		}
		progress(i+1, total)
	}
	e, err := d.episode(ctx, j.EpisodeID)
	if err != nil {
		return nil, err
	}
	if fb := dialogueFit(script.Shots(), durs, chars, e.TargetDurationS); fb != "" {
		return nil, jobs.Errorf(jobs.ErrScriptDuration, nil, "%s", fb)
	}
	return out.persist(nil), nil
}

func checkClip(wav []byte, text, what string) (media.PCM, error) {
	pcm, err := media.ParseWAV(wav)
	if err != nil {
		return pcm, jobs.Errorf(jobs.ErrValidationFailed, err, "%s: voice service returned unusable audio", what)
	}
	secs := pcm.Duration().Seconds()
	if secs <= 0.2 {
		return pcm, jobs.Errorf(jobs.ErrValidationFailed, nil, "%s: audio is empty or too short (%.2f s)", what, secs)
	}
	rate := float64(utf8.RuneCountInString(text)) / secs
	if rate < minClipCharsPerSecond || rate > maxClipCharsPerSecond {
		return pcm, jobs.Errorf(jobs.ErrValidationFailed, nil, "%s: %.1f s of audio for %d characters looks like a failed generation", what, secs, utf8.RuneCountInString(text))
	}
	return pcm, nil
}
