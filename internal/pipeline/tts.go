package pipeline

import (
	"context"
	"strconv"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/media"
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
	if err := d.GPU.Acquire(ctx, gpuHolder(d.Voice)); err != nil {
		return nil, err
	}
	info := d.Voice.Info()
	if err := d.Queue.SetProvider(ctx, j.ID, info.Provider, info.Model); err != nil {
		return nil, err
	}
	lines := script.Lines()
	out := d.newOutputs(j)
	refs := map[string][]byte{}
	refArts := map[string]*artifacts.Artifact{}
	total := len(lines)
	for i, l := range lines {
		progress(i, total)
		if _, ok := refs[l.Speaker]; !ok {
			c, _ := script.Character(l.Speaker)
			wav, err := d.Voice.Design(ctx, c.Voice, referenceText, int(j.EpisodeID))
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
		wav, err := d.Voice.Clone(ctx, refs[l.Speaker], l.Text, int(j.EpisodeID)*1000+i)
		if err != nil {
			return nil, err
		}
		pcm, err := checkClip(wav, l.Text, "line "+strconv.Itoa(i+1))
		if err != nil {
			return nil, err
		}
		meta := map[string]any{"speaker": l.Speaker, "text": l.Text, "shot": l.Shot, "duration_ms": pcm.Duration().Milliseconds(), "sample_rate": pcm.SampleRate}
		if _, err := out.bytes(artifacts.LineAudio, lineKey(i), wav, info, meta, []int64{scriptArt.ID}, refArts[l.Speaker]); err != nil {
			return nil, err
		}
		progress(i+1, total)
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
