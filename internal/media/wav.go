// Package media holds WAV handling and the FFmpeg/ffprobe runners.
package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// PCM is mono 16-bit audio. Every voice clip in the pipeline is normalised to this.
type PCM struct {
	SampleRate int
	Samples    []int16
}

func (p PCM) Duration() time.Duration {
	if p.SampleRate == 0 {
		return 0
	}
	return time.Duration(len(p.Samples)) * time.Second / time.Duration(p.SampleRate)
}

// ParseWAV reads a RIFF/WAVE file holding 16-bit mono PCM and rejects anything else.
func ParseWAV(data []byte) (PCM, error) {
	r := bytes.NewReader(data)
	var hdr struct {
		RIFF [4]byte
		Size uint32
		WAVE [4]byte
	}
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil || string(hdr.RIFF[:]) != "RIFF" || string(hdr.WAVE[:]) != "WAVE" {
		return PCM{}, errors.New("not a WAV file")
	}
	var p PCM
	gotFmt := false
	for {
		var ch struct {
			ID   [4]byte
			Size uint32
		}
		if err := binary.Read(r, binary.LittleEndian, &ch); err != nil {
			if errors.Is(err, io.EOF) {
				return PCM{}, errors.New("WAV has no data chunk")
			}
			return PCM{}, err
		}
		switch string(ch.ID[:]) {
		case "fmt ":
			var f struct {
				Format, Channels          uint16
				SampleRate, ByteRate      uint32
				BlockAlign, BitsPerSample uint16
			}
			if err := binary.Read(r, binary.LittleEndian, &f); err != nil {
				return PCM{}, err
			}
			if f.Format != 1 || f.Channels != 1 || f.BitsPerSample != 16 {
				return PCM{}, fmt.Errorf("WAV must be 16-bit mono PCM, got format=%d channels=%d bits=%d", f.Format, f.Channels, f.BitsPerSample)
			}
			p.SampleRate = int(f.SampleRate)
			gotFmt = true
			if _, err := r.Seek(int64(ch.Size)-16, io.SeekCurrent); err != nil {
				return PCM{}, err
			}
		case "data":
			if !gotFmt {
				return PCM{}, errors.New("WAV data before fmt chunk")
			}
			n := int(ch.Size) / 2
			if n > r.Len()/2 {
				n = r.Len() / 2
			}
			p.Samples = make([]int16, n)
			if err := binary.Read(r, binary.LittleEndian, p.Samples); err != nil {
				return PCM{}, err
			}
			return p, nil
		default:
			if _, err := r.Seek(int64(ch.Size+ch.Size%2), io.SeekCurrent); err != nil {
				return PCM{}, err
			}
		}
	}
}

func WriteWAV(w io.Writer, p PCM) error {
	dataLen := uint32(len(p.Samples) * 2)
	hdr := struct {
		RIFF          [4]byte
		Size          uint32
		WAVE, Fmt     [4]byte
		FmtSize       uint32
		Format, Chans uint16
		Rate, Bytes   uint32
		Align, Bits   uint16
		Data          [4]byte
		DataSize      uint32
	}{
		[4]byte{'R', 'I', 'F', 'F'}, 36 + dataLen, [4]byte{'W', 'A', 'V', 'E'}, [4]byte{'f', 'm', 't', ' '},
		16, 1, 1, uint32(p.SampleRate), uint32(p.SampleRate * 2), 2, 16, [4]byte{'d', 'a', 't', 'a'}, dataLen,
	}
	if err := binary.Write(w, binary.LittleEndian, hdr); err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, p.Samples)
}

// Silence returns d of silence at rate.
func Silence(rate int, d time.Duration) []int16 {
	return make([]int16, int(d.Seconds()*float64(rate)))
}
