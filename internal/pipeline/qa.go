package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/media"
	"github.com/fiat/content-studio/internal/providers"
)

type Severity string

const (
	Blocking Severity = "blocking"
	Warning  Severity = "warning"
)

// QAResult is one rule's outcome. Rule versions change when a rule's logic or threshold changes.
type QAResult struct {
	Rule         string   `json:"rule"`
	RuleVersion  int      `json:"rule_version"`
	Severity     Severity `json:"severity"`
	Passed       bool     `json:"passed"`
	Reason       string   `json:"reason"`
	SuggestedFix string   `json:"suggested_fix,omitempty"`
}

var qaInfo = providers.Info{Provider: "studio", Model: "qa-rules-v1"}

const minFreeDiskBytes = 1 << 30

type qaStage struct{ d *Deps }

func (s *qaStage) Run(ctx context.Context, j *jobs.Job, progress func(int, int)) (func(pgx.Tx) error, error) {
	d := s.d
	if err := d.Queue.SetProvider(ctx, j.ID, qaInfo.Provider, qaInfo.Model); err != nil {
		return nil, err
	}
	e, err := d.episode(ctx, j.EpisodeID)
	if err != nil {
		return nil, err
	}
	videos, err := artifacts.ForRun(ctx, d.DB, j.RunID, artifacts.Video)
	if err != nil {
		return nil, err
	}
	specs, err := artifacts.ForRun(ctx, d.DB, j.RunID, artifacts.RenderSpec)
	if err != nil {
		return nil, err
	}
	if len(videos) == 0 || len(specs) == 0 {
		return nil, jobs.Errorf(jobs.ErrInvalidInput, nil, "this run has no rendered video to check")
	}
	video, specArt := videos[len(videos)-1], specs[len(specs)-1]
	results := d.runQA(ctx, j, e, video, specArt, progress)
	report, err := json.MarshalIndent(map[string]any{"video_artifact_id": video.ID, "video_sha256": video.SHA256, "results": results}, "", "  ")
	if err != nil {
		return nil, err
	}
	var failed []string
	for _, r := range results {
		if r.Severity == Blocking && !r.Passed {
			failed = append(failed, r.Rule)
		}
	}
	status := "qa_passed"
	if len(failed) > 0 {
		status = "qa_failed"
	}
	out := d.newOutputs(j)
	if _, err := out.bytes(artifacts.QAReport, "", report, qaInfo, map[string]any{"passed": len(failed) == 0, "failed_rules": failed}, []int64{video.ID, specArt.ID}); err != nil {
		return nil, err
	}
	record := out.persist(func(tx pgx.Tx) error {
		for _, r := range results {
			if _, err := tx.Exec(ctx, `INSERT INTO qa_results (artifact_id, rule, rule_version, severity, passed, reason, suggested_fix)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, video.ID, r.Rule, r.RuleVersion, r.Severity, r.Passed, r.Reason, r.SuggestedFix); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE artifacts SET validation_status = $1 WHERE id = $2`, status, video.ID)
		return err
	})
	if len(failed) == 0 {
		return record, nil
	}
	// A failed QA is still recorded, so the reasons are visible, then the job fails.
	if err := pgx.BeginFunc(ctx, d.DB, record); err != nil {
		return nil, err
	}
	return nil, jobs.Errorf(jobs.ErrQAFailed, nil, "blocking QA checks failed: %s", strings.Join(failed, ", "))
}

func (d *Deps) runQA(ctx context.Context, j *jobs.Job, e Episode, video, specArt artifacts.Artifact, progress func(int, int)) []QAResult {
	var res []QAResult
	add := func(rule string, ver int, sev Severity, ok bool, reason, fix string) {
		res = append(res, QAResult{Rule: rule, RuleVersion: ver, Severity: sev, Passed: ok, Reason: reason, SuggestedFix: fix})
	}
	st := d.Settings
	const steps = 5
	progress(0, steps)

	path, err := d.Store.Abs(video.StoragePath)
	if err != nil {
		add("file_exists", 1, Blocking, false, "video path is invalid", "re-render")
		return res
	}
	sum, err := artifacts.HashFile(path)
	add("file_exists", 1, Blocking, err == nil && sum == video.SHA256, hashReason(err, sum == video.SHA256), "re-render the episode")
	if err != nil {
		return res
	}

	stderr, derr := d.Tools.Run(ctx, "", "-v", "error", "-i", path, "-f", "null", "-")
	add("decodes", 1, Blocking, derr == nil && strings.TrimSpace(stderr) == "", okOr(derr == nil && strings.TrimSpace(stderr) == "", "full decode finished without errors", "decoder reported errors"), "re-render the episode")
	progress(1, steps)

	p, perr := d.Tools.Probe(ctx, path)
	if perr != nil {
		add("probe", 1, Blocking, false, "ffprobe could not read the file", "re-render the episode")
		return res
	}
	var vcodec, pix, acodec, rate string
	var w, h int
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			vcodec, pix, rate, w, h = s.CodecName, s.PixFmt, s.RFrameRate, s.Width, s.Height
		case "audio":
			acodec = s.CodecName
		}
	}
	add("dimensions", 1, Blocking, w == st.OutWidth && h == st.OutHeight, fmt.Sprintf("%dx%d (expected %dx%d, 9:16)", w, h, st.OutWidth, st.OutHeight), "check the render output size")
	fps := media.FrameRate(rate)
	add("frame_rate", 1, Blocking, math.Abs(fps-float64(st.FPS)) < 0.01, fmt.Sprintf("%.3f fps (expected %d)", fps, st.FPS), "check the render fps setting")
	add("video_stream", 1, Blocking, vcodec == "h264" && pix == "yuv420p", fmt.Sprintf("video %q %q (expected h264 yuv420p)", vcodec, pix), "re-render")
	add("audio_stream", 1, Blocking, acodec == "aac", fmt.Sprintf("audio %q (expected aac)", orNone(acodec)), "re-render")
	dur := p.DurationSeconds()
	target := float64(e.TargetDurationS)
	add("duration", 1, Blocking, math.Abs(dur-target) <= target*durationTolPct/100,
		fmt.Sprintf("%.2f s, target %d s ±%d%%", dur, e.TargetDurationS, durationTolPct), "adjust the script length or the target and run again")
	progress(2, steps)

	var spec RenderSpec
	specData, err := d.read(specArt)
	if err == nil {
		err = json.Unmarshal(specData, &spec)
	}
	if err != nil {
		add("assets", 1, Blocking, false, "render spec is missing or unreadable", "re-render")
	} else {
		missing := 0
		refs := []AssetRef{spec.Dialogue, spec.Subtitles.AssetRef}
		for _, sh := range spec.Shots {
			refs = append(refs, sh.Image)
		}
		for _, r := range refs {
			if got, err := d.Store.Hash(r.Path); err != nil || got != r.SHA256 {
				missing++
			}
		}
		add("assets", 1, Blocking, missing == 0, fmt.Sprintf("%d of %d render assets missing or changed", missing, len(refs)), "regenerate the run")
		add("timeline_match", 1, Blocking, math.Abs(dur*1000-float64(spec.Output.DurationMS)) <= 200,
			fmt.Sprintf("video %.2f s vs timeline %.2f s", dur, float64(spec.Output.DurationMS)/1000), "re-render")
		subOK := false
		if data, err := d.Store.Abs(spec.Subtitles.Path); err == nil {
			if b, err := readSmall(data); err == nil && strings.Contains(b, "-->") {
				subOK = true
			}
		}
		add("subtitles_present", 1, Blocking, subOK, okOr(subOK, "subtitle file present with cues", "subtitle file missing or empty"), "rerun the subtitles stage")
	}

	clips, err := d.inputs(ctx, j, artifacts.LineAudio)
	zero := 0
	for _, c := range clips {
		if ms, _ := c.Meta["duration_ms"].(float64); ms <= 0 {
			zero++
		}
	}
	add("line_audio", 1, Blocking, err == nil && zero == 0, fmt.Sprintf("%d of %d line clips are empty", zero, len(clips)), "rerun the tts stage")

	var fs syscall.Statfs_t
	free := uint64(0)
	if root, err := d.Store.Abs("."); err == nil && syscall.Statfs(root, &fs) == nil {
		free = fs.Bavail * uint64(fs.Bsize)
	}
	add("disk_space", 1, Blocking, free >= minFreeDiskBytes, fmt.Sprintf("%.1f GiB free at the storage root", float64(free)/(1<<30)), "free disk space")
	progress(3, steps)

	lufs, err := d.loudness(ctx, path)
	if err == nil {
		add("loudness", 1, Warning, lufs >= -16 && lufs <= -12, fmt.Sprintf("integrated %.1f LUFS (aim -14)", lufs), "check the loudnorm step")
	}
	black, silence, err := d.blackAndSilence(ctx, path)
	if err == nil {
		add("black_frames", 1, Warning, black <= dur*0.1, fmt.Sprintf("%.1f s of black frames", black), "check the shot images")
		add("long_silence", 1, Warning, silence <= 4, fmt.Sprintf("longest silence %.1f s", silence), "tighten pauses or add lines")
	}
	progress(5, steps)
	return res
}

var lufsRe = regexp.MustCompile(`I:\s+(-?[0-9.]+) LUFS`)

func (d *Deps) loudness(ctx context.Context, path string) (float64, error) {
	stderr, err := d.Tools.Run(ctx, "", "-nostats", "-i", path, "-vn", "-af", "ebur128=framelog=quiet", "-f", "null", "-")
	if err != nil {
		return 0, err
	}
	m := lufsRe.FindAllStringSubmatch(stderr, -1)
	if len(m) == 0 {
		return 0, fmt.Errorf("no loudness summary")
	}
	return strconv.ParseFloat(m[len(m)-1][1], 64)
}

var (
	blackRe   = regexp.MustCompile(`black_duration:([0-9.]+)`)
	silenceRe = regexp.MustCompile(`silence_duration: ([0-9.]+)`)
)

func (d *Deps) blackAndSilence(ctx context.Context, path string) (black, longestSilence float64, err error) {
	stderr, err := d.Tools.Run(ctx, "", "-nostats", "-i", path, "-vf", "blackdetect=d=0.5:pix_th=0.10", "-af", "silencedetect=n=-50dB:d=1", "-f", "null", "-")
	if err != nil {
		return 0, 0, err
	}
	for _, m := range blackRe.FindAllStringSubmatch(stderr, -1) {
		v, _ := strconv.ParseFloat(m[1], 64)
		black += v
	}
	for _, m := range silenceRe.FindAllStringSubmatch(stderr, -1) {
		v, _ := strconv.ParseFloat(m[1], 64)
		longestSilence = max(longestSilence, v)
	}
	return black, longestSilence, nil
}

func hashReason(err error, match bool) string {
	switch {
	case err != nil:
		return "video file is missing"
	case !match:
		return "video file does not match its recorded sha256"
	}
	return "file present and sha256 matches"
}

func okOr(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func readSmall(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
