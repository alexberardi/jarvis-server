// Package audio holds small, dependency-free audio helpers (WAV I/O, sample conversion).
package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// ReadWAV decodes a 16-bit PCM WAV (any channel count, downmixed to mono) into [-1, 1] samples.
func ReadWAV(r io.Reader) (samples []float32, sampleRate int, err error) {
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil {
		return nil, 0, fmt.Errorf("wav: header: %w", err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return nil, 0, errors.New("wav: not a RIFF/WAVE file")
	}
	var channels, bits int
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return nil, 0, fmt.Errorf("wav: no data chunk: %w", err)
		}
		id, size := string(ch[0:4]), int(binary.LittleEndian.Uint32(ch[4:8]))
		switch id {
		case "fmt ":
			b := make([]byte, size)
			if _, err := io.ReadFull(r, b); err != nil {
				return nil, 0, err
			}
			if format := binary.LittleEndian.Uint16(b[0:2]); format != 1 && format != 0xFFFE {
				return nil, 0, fmt.Errorf("wav: unsupported format %d (want PCM)", format)
			}
			channels = int(binary.LittleEndian.Uint16(b[2:4]))
			sampleRate = int(binary.LittleEndian.Uint32(b[4:8]))
			bits = int(binary.LittleEndian.Uint16(b[14:16]))
			if bits != 16 || channels < 1 {
				return nil, 0, fmt.Errorf("wav: unsupported %d-bit/%d-channel audio (want 16-bit PCM)", bits, channels)
			}
		case "data":
			if channels == 0 {
				return nil, 0, errors.New("wav: data before fmt chunk")
			}
			b := make([]byte, size)
			n, err := io.ReadFull(r, b)
			if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, 0, err
			}
			frames := n / (2 * channels)
			samples = make([]float32, frames)
			for i := 0; i < frames; i++ {
				var sum float32
				for c := 0; c < channels; c++ {
					off := 2 * (i*channels + c)
					sum += float32(int16(binary.LittleEndian.Uint16(b[off:]))) / 32768
				}
				samples[i] = sum / float32(channels)
			}
			return samples, sampleRate, nil
		default:
			if _, err := io.CopyN(io.Discard, r, int64(size+size%2)); err != nil {
				return nil, 0, err
			}
		}
	}
}

// WriteWAV encodes mono [-1, 1] samples as 16-bit PCM WAV.
func WriteWAV(w io.Writer, samples []float32, sampleRate int) error {
	if err := writeHeader(w, len(samples)*2, 1, sampleRate); err != nil {
		return err
	}
	buf := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[2*i:], uint16(FloatToPCM16(s)))
	}
	_, err := w.Write(buf)
	return err
}

// FloatToPCM16 converts a [-1, 1] sample to int16 with clipping.
func FloatToPCM16(s float32) int16 {
	v := math.Round(float64(s) * 32767)
	return int16(max(-32768, min(32767, v)))
}

func writeHeader(w io.Writer, dataBytes, channels, sampleRate int) error {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+dataBytes))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(h[28:], uint32(sampleRate*channels*2))
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(dataBytes))
	_, err := w.Write(h)
	return err
}
