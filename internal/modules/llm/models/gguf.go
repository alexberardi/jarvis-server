package models

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// GGUF metadata value types (gguf.md in ggml).
const (
	ggufUint8 uint32 = iota
	ggufInt8
	ggufUint16
	ggufInt16
	ggufUint32
	ggufInt32
	ggufFloat32
	ggufBool
	ggufString
	ggufArray
	ggufUint64
	ggufInt64
	ggufFloat64
)

// ggufMaxString caps a key or string value read into memory, so a corrupt header can't
// ask for a huge allocation (real chat templates are tens of KB).
const ggufMaxString = 16 << 20

// GGUFChatTemplate returns the tokenizer.chat_template a GGUF file carries in its header (""
// when it has none). It reads only the metadata, never the tensors.
func GGUFChatTemplate(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var hdr struct {
		Magic   [4]byte
		Version uint32
		Tensors uint64
		KVs     uint64
	}
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil {
		return "", fmt.Errorf("gguf %s: header: %w", path, err)
	}
	if string(hdr.Magic[:]) != "GGUF" {
		return "", fmt.Errorf("gguf %s: not a GGUF file", path)
	}
	if hdr.Version != 2 && hdr.Version != 3 {
		return "", fmt.Errorf("gguf %s: unsupported version %d", path, hdr.Version)
	}
	for i := uint64(0); i < hdr.KVs; i++ {
		key, err := ggufReadString(r)
		if err != nil {
			return "", fmt.Errorf("gguf %s: key %d: %w", path, i, err)
		}
		var typ uint32
		if err := binary.Read(r, binary.LittleEndian, &typ); err != nil {
			return "", fmt.Errorf("gguf %s: %s: %w", path, key, err)
		}
		if key == "tokenizer.chat_template" && typ == ggufString {
			s, err := ggufReadString(r)
			if err != nil {
				return "", fmt.Errorf("gguf %s: %s: %w", path, key, err)
			}
			return s, nil
		}
		if err := ggufSkip(r, typ, 0); err != nil {
			return "", fmt.Errorf("gguf %s: %s: %w", path, key, err)
		}
	}
	return "", nil
}

func ggufReadString(r io.Reader) (string, error) {
	var n uint64
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return "", err
	}
	if n > ggufMaxString {
		return "", fmt.Errorf("string of %d bytes", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

// ggufSize is the byte size of a fixed-size value type (0 for string and array).
func ggufSize(typ uint32) (int64, bool) {
	switch typ {
	case ggufUint8, ggufInt8, ggufBool:
		return 1, true
	case ggufUint16, ggufInt16:
		return 2, true
	case ggufUint32, ggufInt32, ggufFloat32:
		return 4, true
	case ggufUint64, ggufInt64, ggufFloat64:
		return 8, true
	}
	return 0, false
}

// ggufSkip discards one value of type typ without keeping it.
func ggufSkip(r io.Reader, typ uint32, depth int) error {
	if n, ok := ggufSize(typ); ok {
		return ggufDiscard(r, n)
	}
	switch typ {
	case ggufString:
		var n uint64
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return err
		}
		return ggufDiscard(r, int64(min(n, 1<<62)))
	case ggufArray:
		if depth > 8 {
			return errors.New("arrays nested too deep")
		}
		var elem uint32
		var count uint64
		if err := binary.Read(r, binary.LittleEndian, &elem); err != nil {
			return err
		}
		if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
			return err
		}
		if n, ok := ggufSize(elem); ok {
			if count > (1<<62)/uint64(n) {
				return fmt.Errorf("array of %d values", count)
			}
			return ggufDiscard(r, n*int64(count))
		}
		for j := uint64(0); j < count; j++ {
			if err := ggufSkip(r, elem, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("unknown value type %d", typ)
}

func ggufDiscard(r io.Reader, n int64) error {
	got, err := io.CopyN(io.Discard, r, n)
	if err == io.EOF && got < n {
		return io.ErrUnexpectedEOF
	}
	return err
}
