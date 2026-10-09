// Package wire implements the bounded JSON framing shared by the SSH desktop
// app, its local controller, and the remote connector. Framing is not
// authentication: callers must separately verify the transport peer and validate
// operation-specific schemas before invoking any action. Transport owners must
// also impose read/write deadlines and connection concurrency limits.
package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const (
	// MaxFrameBytes is checked before allocating the payload buffer.
	MaxFrameBytes = 1 << 20
	// MaxDepth bounds object/array nesting independently of the byte limit.
	MaxDepth = 64
)

var (
	// ErrFrameSize reports a zero-length or oversized frame.
	ErrFrameSize = errors.New("ssh wire: invalid frame size")
	// ErrInvalidJSON reports malformed or ambiguous JSON without payload text.
	ErrInvalidJSON = errors.New("ssh wire: invalid JSON object")
)

// ReadFrame reads exactly one frame. On any non-EOF error the caller must close
// the connection, rather than attempt to resynchronize or dispatch partial data.
// JSON errors deliberately omit payload fragments, which may contain prompts.
func ReadFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameBytes {
		return nil, ErrFrameSize
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r, payload); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	if err := Validate(payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// WriteFrame validates before writing any bytes. A partial transport write is an
// uncertain delivery, not permission to replay an execution on a new connection.
// One writer must serialize frames for a connection; this function does not lock.
func WriteFrame(w io.Writer, payload []byte) error {
	if err := Validate(payload); err != nil {
		return err
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, payload)
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// Validate accepts one UTF-8 JSON object, rejecting duplicate keys at every
// nesting level (including escaped equivalents), extra JSON values and excessive
// depth. It does not deserialize numbers into floating point values.
func Validate(payload []byte) error {
	if len(payload) == 0 || len(payload) > MaxFrameBytes {
		return ErrFrameSize
	}
	if !utf8.Valid(payload) {
		return ErrInvalidJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalidJSON
	}
	if err := object(decoder, 1); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidJSON
	}
	return nil
}

func object(decoder *json.Decoder, depth int) error {
	if depth > MaxDepth {
		return ErrInvalidJSON
	}
	keys := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return ErrInvalidJSON
		}
		key, ok := token.(string)
		if !ok {
			return ErrInvalidJSON
		}
		if _, exists := keys[key]; exists {
			return ErrInvalidJSON
		}
		keys[key] = struct{}{}
		if err := value(decoder, depth); err != nil {
			return err
		}
	}
	token, err := decoder.Token()
	if err != nil || token != json.Delim('}') {
		return ErrInvalidJSON
	}
	return nil
}

func value(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return ErrInvalidJSON
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return object(decoder, depth+1)
	case '[':
		if depth+1 > MaxDepth {
			return ErrInvalidJSON
		}
		for decoder.More() {
			if err := value(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return ErrInvalidJSON
		}
		return nil
	default:
		return ErrInvalidJSON
	}
}
