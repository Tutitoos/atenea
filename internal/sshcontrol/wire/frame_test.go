package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFramesPreserveBytesAndBoundaries(t *testing.T) {
	first := []byte(`{"prompt":"literal  spacing \n café","body":{"n":1}}`)
	second := []byte(`{"operation":"status"}`)
	var stream bytes.Buffer
	for _, payload := range [][]byte{first, second} {
		if err := WriteFrame(&stream, payload); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range [][]byte{first, second} {
		got, err := ReadFrame(&stream)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("round trip: %v", err)
		}
	}
	if _, err := ReadFrame(&stream); !errors.Is(err, io.EOF) {
		t.Fatalf("end: %v", err)
	}
}

func TestInvalidFramesWriteNothing(t *testing.T) {
	cases := []string{
		"", `[]`, `null`, `{"a":1,"a":2}`, `{"a":1,"\u0061":2}`,
		`{"nested":[{"x":1,"x":2}]}`, `{"ok":1} {"extra":2}`,
		`{"secret":"DO_NOT_LOG",}`, `{"n":NaN}`, `{"n":01}`, "{\"x\":\"\xff\"}",
		`{"x":` + strings.Repeat("[", MaxDepth) + "0" + strings.Repeat("]", MaxDepth) + "}",
		`{"x":"` + strings.Repeat("a", MaxFrameBytes) + `"}`,
	}
	for index, input := range cases {
		var dst bytes.Buffer
		err := WriteFrame(&dst, []byte(input))
		if err == nil || dst.Len() != 0 {
			t.Fatalf("case %d accepted or wrote partial frame", index)
		}
		if strings.Contains(err.Error(), "DO_NOT_LOG") {
			t.Fatal("payload in error")
		}
	}
}

type headerOnlyReader struct {
	header       *bytes.Reader
	payloadReads int
}

func (r *headerOnlyReader) Read(p []byte) (int, error) {
	if r.header.Len() == 0 {
		r.payloadReads++
		return 0, errors.New("payload must not be read")
	}
	return r.header.Read(p)
}

func TestOversizeRejectedBeforePayloadRead(t *testing.T) {
	for _, size := range []uint32{0, MaxFrameBytes + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		reader := &headerOnlyReader{header: bytes.NewReader(header[:])}
		payload, err := ReadFrame(reader)
		if !errors.Is(err, ErrFrameSize) || payload != nil || reader.payloadReads != 0 {
			t.Fatal("invalid length reached payload reader")
		}
	}
}

func TestTruncationNeverReturnsPayload(t *testing.T) {
	var stream bytes.Buffer
	if err := WriteFrame(&stream, []byte(`{"prompt":"private"}`)); err != nil {
		t.Fatal(err)
	}
	complete := stream.Bytes()
	for n := 1; n < len(complete); n++ {
		payload, err := ReadFrame(bytes.NewReader(complete[:n]))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("prefix %d: %v", n, err)
		}
		if payload != nil {
			t.Fatal("returned truncated payload")
		}
	}
}

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > 2 {
		p = p[:2]
	}
	return w.Buffer.Write(p)
}

type stuckWriter struct{}

func (stuckWriter) Write([]byte) (int, error) { return 0, nil }

func TestShortWritesAndStalledWriter(t *testing.T) {
	var writer shortWriter
	payload := []byte(`{"operation":"hello"}`)
	if err := WriteFrame(&writer, payload); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&writer)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("short writes: %v", err)
	}
	if err := WriteFrame(stuckWriter{}, payload); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("stalled writer: %v", err)
	}
}

func FuzzReadFrame(f *testing.F) {
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Add([]byte{255, 255, 255, 255})
	f.Fuzz(func(t *testing.T, input []byte) {
		payload, err := ReadFrame(bytes.NewReader(input))
		if err != nil {
			if payload != nil {
				t.Fatal("partial payload on error")
			}
			return
		}
		if len(payload) > MaxFrameBytes {
			t.Fatal("oversized frame")
		}
		var output bytes.Buffer
		if err := WriteFrame(&output, payload); err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(input, output.Bytes()) {
			t.Fatal("framing changed input")
		}
	})
}
