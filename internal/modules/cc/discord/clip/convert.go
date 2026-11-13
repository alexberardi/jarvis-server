package clip

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/discord/opusnative"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
)

// OggToWAV decodes a Discord voice message to a 48 kHz stereo WAV for speech to text, which
// resamples whatever it is given.
func OggToWAV(ogg []byte) ([]byte, error) {
	packets, err := ReadOpusPackets(ogg)
	if err != nil {
		return nil, err
	}
	dec, err := opusnative.NewDecoder()
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	var pcm bytes.Buffer
	for _, p := range packets {
		s, err := dec.Decode(p)
		if err != nil {
			return nil, err
		}
		_ = binary.Write(&pcm, binary.LittleEndian, s)
	}
	if pcm.Len() == 0 {
		return nil, errors.New("clip: no audio in the clip")
	}
	var wav bytes.Buffer
	if err := audio.WritePCM16WAV(&wav, pcm.Bytes(), opusnative.Channels, opusnative.SampleRate); err != nil {
		return nil, err
	}
	return wav.Bytes(), nil
}

// PCMToOgg encodes 16-bit little-endian PCM (as the TTS engine makes it) as an Ogg Opus voice
// message, and says how long it is in seconds.
func PCMToOgg(pcm []byte, channels, rate int) (ogg []byte, seconds float64, err error) {
	packets, seconds, err := PCMToPackets(pcm, channels, rate)
	if err != nil {
		return nil, 0, err
	}
	return WriteOpusPackets(packets), seconds, nil
}

// PCMToWAV wraps 48 kHz stereo samples (a decoded Discord voice stream) as a WAV for speech to text.
func PCMToWAV(pcm []int16) ([]byte, error) {
	var raw bytes.Buffer
	_ = binary.Write(&raw, binary.LittleEndian, pcm)
	var wav bytes.Buffer
	if err := audio.WritePCM16WAV(&wav, raw.Bytes(), opusnative.Channels, opusnative.SampleRate); err != nil {
		return nil, err
	}
	return wav.Bytes(), nil
}

// PCMToPackets encodes 16-bit little-endian PCM as 20 ms stereo 48 kHz Opus packets, the unit a
// Discord voice channel sends, and says how long the audio is in seconds.
func PCMToPackets(pcm []byte, channels, rate int) (packets [][]byte, seconds float64, err error) {
	if channels < 1 || rate < 8000 || len(pcm) < 2*channels {
		return nil, 0, errors.New("clip: no audio to encode")
	}
	n := len(pcm) / 2 / channels
	mono := make([]float32, n)
	for i := 0; i < n; i++ {
		var sum float32
		for c := 0; c < channels; c++ {
			sum += float32(int16(binary.LittleEndian.Uint16(pcm[(i*channels+c)*2:]))) / 32768
		}
		mono[i] = sum / float32(channels)
	}
	if rate != opusnative.SampleRate {
		mono = stt.Resample(mono, rate, opusnative.SampleRate)
	}
	enc, err := opusnative.NewEncoder()
	if err != nil {
		return nil, 0, err
	}
	defer enc.Close()
	frame := make([]int16, opusnative.FrameSize*opusnative.Channels)
	for off := 0; off < len(mono); off += opusnative.FrameSize {
		for i := 0; i < opusnative.FrameSize; i++ {
			var v float32
			if off+i < len(mono) { // the last frame is padded with silence
				v = mono[off+i]
			}
			s := int16(max(-1, min(1, v)) * 32767)
			frame[2*i], frame[2*i+1] = s, s
		}
		p, err := enc.Encode(frame)
		if err != nil {
			return nil, 0, err
		}
		packets = append(packets, append([]byte(nil), p...))
	}
	return packets, float64(len(mono)) / opusnative.SampleRate, nil
}
