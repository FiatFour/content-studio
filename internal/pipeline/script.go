package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
)

//go:embed script.schema.json
var scriptSchemaJSON []byte

var scriptSchema = mustCompile(scriptSchemaJSON)

func mustCompile(raw []byte) *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		panic(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("script.schema.json", doc); err != nil {
		panic(err)
	}
	return c.MustCompile("script.schema.json")
}

type Script struct {
	Title      string      `json:"title"`
	Characters []Character `json:"characters"`
	Scenes     []Scene     `json:"scenes"`
}

type Character struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Look  string `json:"look"`
	Voice string `json:"voice"`
}

type Scene struct {
	Setting string `json:"setting"`
	Shots   []Shot `json:"shots"`
}

type Shot struct {
	ImagePrompt string `json:"image_prompt"`
	Camera      string `json:"camera"`
	Lines       []Line `json:"lines"`
}

type Line struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// FlatShot is a shot with its position in the episode and its scene's setting.
type FlatShot struct {
	Index   int
	Setting string
	Shot
}

func (s *Script) Shots() []FlatShot {
	var out []FlatShot
	for _, sc := range s.Scenes {
		for _, sh := range sc.Shots {
			out = append(out, FlatShot{Index: len(out), Setting: sc.Setting, Shot: sh})
		}
	}
	return out
}

// FlatLine is a dialogue line with its shot and its position in the episode.
type FlatLine struct {
	Index, Shot int
	Line
}

func (s *Script) Lines() []FlatLine {
	var out []FlatLine
	for _, sh := range s.Shots() {
		for _, l := range sh.Lines {
			out = append(out, FlatLine{Index: len(out), Shot: sh.Index, Line: l})
		}
	}
	return out
}

func (s *Script) Character(id string) (Character, bool) {
	for _, c := range s.Characters {
		if c.ID == id {
			return c, true
		}
	}
	return Character{}, false
}

// Dialogue length per second of target episode. VoxCPM2 Thai speech measured 11.6 characters per
// second in the first real run (350 characters gave 30 s), and pauses, lead and tail take about
// 10 s of a 60 s episode. A script outside this range cannot land within the +-10% duration gate.
const (
	minCharsPerSecond = 8.0
	maxCharsPerSecond = 11.0
)

// bannedInPrompts keeps existing shows and studios out of image prompts (product.md: originality).
var bannedInPrompts = []string{"simpson", "family guy", "futurama", "south park", "bob's burgers", "rick and morty",
	"king of the hill", "american dad", "disney", "pixar", "dreamworks", "ghibli", "cartoon network", "nickelodeon", "matt groening"}

// ValidateScript parses untrusted model output and checks it against the schema and the episode's
// constraints. Every error message is safe to show and to feed back to the model.
func ValidateScript(raw []byte, targetSeconds int) (*Script, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("output is not valid JSON")
	}
	if err := scriptSchema.Validate(doc); err != nil {
		return nil, fmt.Errorf("output does not match the script schema: %s", schemaDetail(err))
	}
	var s Script
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("output does not match the script schema")
	}
	seen := map[string]bool{}
	for _, c := range s.Characters {
		if seen[c.ID] {
			return nil, fmt.Errorf("character id %q is used twice", c.ID)
		}
		seen[c.ID] = true
	}
	shots, lines := s.Shots(), s.Lines()
	minShots, maxShots := int(math.Ceil(float64(targetSeconds)/12)), int(math.Ceil(float64(targetSeconds)/4))
	if len(shots) < minShots || len(shots) > maxShots {
		return nil, fmt.Errorf("script has %d shots; a %d s episode needs %d to %d", len(shots), targetSeconds, minShots, maxShots)
	}
	chars := 0
	for _, l := range lines {
		if !seen[l.Speaker] {
			return nil, fmt.Errorf("line %d is spoken by %q, who is not in characters", l.Index+1, l.Speaker)
		}
		if !hasThai(l.Text) {
			return nil, fmt.Errorf("line %d is not in Thai", l.Index+1)
		}
		chars += utf8.RuneCountInString(l.Text)
	}
	minChars, maxChars := int(float64(targetSeconds)*minCharsPerSecond), int(float64(targetSeconds)*maxCharsPerSecond)
	if chars < minChars {
		perLine := max(chars/max(len(lines), 1), 20)
		return nil, fmt.Errorf("dialogue has only %d Thai characters but a %d s episode needs at least %d. Add about %d more characters: write at least %d more dialogue lines, or make the lines longer (30 to 80 characters each)",
			chars, targetSeconds, minChars, minChars-chars, (minChars-chars+perLine-1)/perLine)
	}
	if chars > maxChars {
		return nil, fmt.Errorf("dialogue has %d Thai characters but a %d s episode allows at most %d. Remove about %d characters", chars, targetSeconds, maxChars, chars-maxChars)
	}
	for _, sh := range shots {
		p := strings.ToLower(sh.ImagePrompt + " " + sh.Setting)
		for _, b := range bannedInPrompts {
			if strings.Contains(p, b) {
				return nil, fmt.Errorf("shot %d image prompt names an existing franchise (%q); describe an original scene instead", sh.Index+1, b)
			}
		}
	}
	return &s, nil
}

func hasThai(s string) bool {
	for _, r := range s {
		if r >= 0x0E00 && r <= 0x0E7F {
			return true
		}
	}
	return false
}

// schemaDetail returns the first concrete violation ("at '/scenes': minItems: ...") without the
// schema's file URL, so the message is safe to show and useful to feed back to the model.
func schemaDetail(err error) string {
	for _, line := range strings.Split(err.Error(), "\n") {
		if l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-")); strings.HasPrefix(l, "at '") {
			return l
		}
	}
	return "invalid structure"
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

const scriptPromptVersion = 1

func scriptPrompt(e Episode) string {
	return fmt.Sprintf(`You write one episode of an ORIGINAL adult-animated life-simulation sitcom for a vertical phone video.
Episode title: %s
Idea: %s
Target length: %d seconds of finished video.

Rules:
- Dialogue "text" is natural spoken Thai. Everything else (look, voice, setting, image_prompt) is English.
- 2 to 4 characters. "id" is a short lowercase ASCII id. "look" describes appearance for an illustrator. "voice" describes how they sound (age, gender, tone).
- About %d to %d shots in total. Each shot is one illustrated still with a camera move.
- "image_prompt" describes only what is visible in that shot: who, action, expression, place. No text, signs or logos in the picture.
- Never name existing shows, studios or characters.
- Write at least %d dialogue lines in total, each 30 to 80 Thai characters, so the whole dialogue is roughly %d Thai characters. Short one-line replies are not enough: characters talk in full sentences. A shot may have no lines.
- Comedy first, with a small warm or wry ending beat.
Return only the JSON object.`, e.Title, e.Idea, e.TargetDurationS,
		int(math.Ceil(float64(e.TargetDurationS)/9)), int(math.Ceil(float64(e.TargetDurationS)/5)),
		int(math.Ceil(float64(e.TargetDurationS)/5)), int(float64(e.TargetDurationS)*9))
}

type scriptStage struct{ d *Deps }

func (s *scriptStage) Run(ctx context.Context, j *jobs.Job, progress func(int, int)) (func(pgx.Tx) error, error) {
	d := s.d
	e, err := d.episode(ctx, j.EpisodeID)
	if err != nil {
		return nil, err
	}
	if err := d.GPU.Acquire(ctx, gpuHolder(d.LLM)); err != nil {
		return nil, err
	}
	info := d.LLM.Info()
	if err := d.Queue.SetProvider(ctx, j.ID, info.Provider, info.Model); err != nil {
		return nil, err
	}
	base := scriptPrompt(e)
	prompt := base
	tries := max(d.Settings.ScriptRetries, 1)
	var lastErr error
	for i := 0; i < tries; i++ {
		progress(i, tries)
		raw, err := d.LLM.GenerateJSON(ctx, prompt, scriptSchemaJSON, int(j.EpisodeID)*100+j.Attempt*10+i)
		if err != nil {
			return nil, err
		}
		script, verr := ValidateScript(raw, e.TargetDurationS)
		if verr != nil {
			lastErr = verr
			d.Log.Warn("script rejected", "job_id", j.ID, "try", i+1, "reason", verr.Error())
			prompt = base + "\n\nYour previous answer was rejected: " + verr.Error() + ". Fix that and answer again."
			continue
		}
		out := d.newOutputs(j)
		pretty, err := json.MarshalIndent(script, "", "  ")
		if err != nil {
			return nil, err
		}
		promptDoc, err := json.Marshal(map[string]any{"template_version": scriptPromptVersion, "prompt": prompt, "schema": json.RawMessage(scriptSchemaJSON)})
		if err != nil {
			return nil, err
		}
		pa, err := out.bytes(artifacts.ScriptInput, "", promptDoc, info, nil, nil)
		if err != nil {
			return nil, err
		}
		meta := map[string]any{"shots": len(script.Shots()), "lines": len(script.Lines()), "tries": i + 1}
		if _, err := out.bytes(artifacts.Script, "", pretty, info, meta, nil, pa); err != nil {
			return nil, err
		}
		return out.persist(nil), nil
	}
	return nil, jobs.Errorf(jobs.ErrValidationFailed, nil, "script rejected %d times; last reason: %s", tries, lastErr)
}

// gpuHolder returns p as a GPUHolder when it is one, so GPU.Acquire can skip it.
func gpuHolder(p any) interface{ ReleaseGPU(context.Context) error } {
	if h, ok := p.(interface{ ReleaseGPU(context.Context) error }); ok {
		return h
	}
	return nil
}
