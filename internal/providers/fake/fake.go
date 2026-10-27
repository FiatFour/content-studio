// Package fake provides deterministic providers for tests. They never touch a model or the network.
package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fiat/content-studio/internal/media"
	"github.com/fiat/content-studio/internal/providers"
)

// LLM returns Responses in order, repeating the last one.
type LLM struct {
	Responses []string
	Calls     int
}

func (l *LLM) Info() providers.Info { return providers.Info{Provider: "fake", Model: "fake-llm"} }

func (l *LLM) GenerateJSON(context.Context, string, json.RawMessage, int) ([]byte, error) {
	i := min(l.Calls, len(l.Responses)-1)
	l.Calls++
	return []byte(l.Responses[i]), nil
}

func (l *LLM) ReleaseGPU(context.Context) error { return nil }

type Images struct{ Calls int }

func (f *Images) Info() providers.Info { return providers.Info{Provider: "fake", Model: "fake-image"} }

func (f *Images) Generate(_ context.Context, req providers.ImageRequest) ([]byte, error) {
	f.Calls++
	img := image.NewRGBA(image.Rect(0, 0, req.Width, req.Height))
	c := color.RGBA{uint8(req.Seed * 40), 120, 200, 255}
	for y := 0; y < req.Height; y++ {
		for x := 0; x < req.Width; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	return buf.Bytes(), err
}

func (f *Images) ReleaseGPU(context.Context) error { return nil }

// Voice speaks a tone whose length is SecondsPerRune per character of text.
type Voice struct {
	SecondsPerRune float64
	Calls          int
}

const sampleRate = 48000

func (v *Voice) Info() providers.Info { return providers.Info{Provider: "fake", Model: "fake-voice"} }

func (v *Voice) tone(text string) ([]byte, error) {
	v.Calls++
	d := time.Duration(float64(utf8.RuneCountInString(text)) * v.SecondsPerRune * float64(time.Second))
	n := int(d.Seconds() * sampleRate)
	s := make([]int16, n)
	for i := range s {
		s[i] = int16(8000 * math.Sin(2*math.Pi*220*float64(i)/sampleRate))
	}
	var buf bytes.Buffer
	err := media.WriteWAV(&buf, media.PCM{SampleRate: sampleRate, Samples: s})
	return buf.Bytes(), err
}

func (v *Voice) Design(_ context.Context, _, text string, _ int) ([]byte, error) { return v.tone(text) }

func (v *Voice) Clone(_ context.Context, ref []byte, text string, _ int) ([]byte, error) {
	if _, err := media.ParseWAV(ref); err != nil {
		return nil, fmt.Errorf("fake clone got a bad reference: %w", err)
	}
	return v.tone(text)
}

func (v *Voice) ReleaseGPU(context.Context) error { return nil }

// Segmenter splits on spaces and then into runs of at most 4 runes, so long Thai runs still get breaks.
type Segmenter struct{}

func (Segmenter) Segment(_ context.Context, text string) ([]string, error) {
	var out []string
	for i, field := range strings.Split(text, " ") {
		if i > 0 {
			out = append(out, " ")
		}
		r := []rune(field)
		for len(r) > 4 {
			out = append(out, string(r[:4]))
			r = r[4:]
		}
		if len(r) > 0 {
			out = append(out, string(r))
		}
	}
	return out, nil
}
