// Package clip converts Discord voice messages (Ogg Opus) to and from the audio the speech
// services use (WAV, PCM), in pure Go apart from libopus itself (opusnative).
package clip

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
)

var crcTable = func() (t [256]uint32) {
	for i := range t {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return
}()

// oggCRC is Ogg's checksum: CRC-32 with polynomial 0x04c11db7, not reflected, zero start.
func oggCRC(b []byte) (c uint32) {
	for _, x := range b {
		c = c<<8 ^ crcTable[byte(c>>24)^x]
	}
	return
}

// ReadOpusPackets returns the audio packets of an Ogg Opus stream, without the two header packets.
func ReadOpusPackets(data []byte) ([][]byte, error) {
	var packets [][]byte
	var partial []byte
	for len(data) > 0 {
		if len(data) < 27 || string(data[:4]) != "OggS" {
			return nil, errors.New("clip: not an Ogg stream")
		}
		nseg := int(data[26])
		if len(data) < 27+nseg {
			return nil, errors.New("clip: truncated Ogg page")
		}
		table := data[27 : 27+nseg]
		size := 0
		for _, s := range table {
			size += int(s)
		}
		body := data[27+nseg:]
		if len(body) < size {
			return nil, errors.New("clip: truncated Ogg page")
		}
		off := 0
		for _, s := range table {
			partial = append(partial, body[off:off+int(s)]...)
			off += int(s)
			if s < 255 { // a lacing value under 255 ends the packet
				packets = append(packets, partial)
				partial = nil
			}
		}
		data = body[size:]
	}
	if len(packets) < 2 || !bytes.HasPrefix(packets[0], []byte("OpusHead")) {
		return nil, errors.New("clip: not an Opus stream")
	}
	return packets[2:], nil // OpusHead, OpusTags, then audio
}

type pageWriter struct {
	buf    bytes.Buffer
	serial uint32
	seq    uint32
}

func (w *pageWriter) page(headerType byte, granule int64, packets [][]byte) {
	var table []byte
	var body []byte
	for _, p := range packets {
		n := len(p)
		for n >= 255 {
			table = append(table, 255)
			n -= 255
		}
		table = append(table, byte(n))
		body = append(body, p...)
	}
	h := make([]byte, 27, 27+len(table))
	copy(h, "OggS")
	h[5] = headerType
	binary.LittleEndian.PutUint64(h[6:], uint64(granule))
	binary.LittleEndian.PutUint32(h[14:], w.serial)
	binary.LittleEndian.PutUint32(h[18:], w.seq)
	h[26] = byte(len(table))
	h = append(h, table...)
	page := append(h, body...)
	binary.LittleEndian.PutUint32(page[22:], oggCRC(page))
	w.buf.Write(page)
	w.seq++
}

const (
	preSkip         = 312 // samples at 48 kHz libopus drops from the start of a stream
	samplesPerFrame = 960 // 20 ms at 48 kHz
	packetsPerPage  = 40  // 255 segments is the page limit; 20 ms packets are far below 255 bytes each
)

// WriteOpusPackets wraps 20 ms Opus packets (stereo, 48 kHz) in an Ogg Opus stream.
func WriteOpusPackets(packets [][]byte) []byte {
	var s [4]byte
	_, _ = rand.Read(s[:])
	w := &pageWriter{serial: binary.LittleEndian.Uint32(s[:])}
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1                                       // version
	head[9] = 2                                       // channels
	binary.LittleEndian.PutUint16(head[10:], preSkip) // pre-skip
	binary.LittleEndian.PutUint32(head[12:], 48000)   // input rate
	w.page(0x02, 0, [][]byte{head})                   // beginning of stream
	tags := append([]byte("OpusTags"), 6, 0, 0, 0, 'j', 'a', 'r', 'v', 'i', 's', 0, 0, 0, 0)
	w.page(0, 0, [][]byte{tags})
	granule := int64(preSkip)
	for i := 0; i < len(packets); i += packetsPerPage {
		end := min(i+packetsPerPage, len(packets))
		granule += int64(end-i) * samplesPerFrame
		t := byte(0)
		if end == len(packets) {
			t = 0x04 // end of stream
		}
		w.page(t, granule, packets[i:end])
	}
	if len(packets) == 0 {
		w.page(0x04, granule, nil)
	}
	return w.buf.Bytes()
}
