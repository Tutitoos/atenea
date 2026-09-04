package procgroup

import (
	"bytes"
	"errors"
	"os/exec"
)

// Capture bounds one process stream. The callback stops its process on overflow.
// Like bytes.Buffer, it has one writer and is read only after that writer exits.
type Capture struct {
	buffer   bytes.Buffer
	exceeded bool
	stop     func()
}

// ErrOutputLimit identifies a process whose captured output exceeded the limit.
var ErrOutputLimit = errors.New("process output exceeds 8 MiB")

// MaxOutput caps a captured stream at eight MiB.
const MaxOutput = 8 << 20

// NewCapture constructs a bounded capture with an overflow callback.
func NewCapture(stop func()) *Capture { return &Capture{stop: stop} }

// Write retains bounded bytes and stops the owned process on overflow.
func (c *Capture) Write(p []byte) (int, error) {
	n := len(p)
	space := MaxOutput - c.buffer.Len()
	if len(p) > space {
		p = p[:space]
		if !c.exceeded {
			c.exceeded = true
			if c.stop != nil {
				c.stop()
			}
		}
	}
	_, _ = c.buffer.Write(p)
	return n, nil
}

// WriteString implements io.StringWriter.
func (c *Capture) WriteString(s string) (int, error) { return c.Write([]byte(s)) }

// String returns the public textual representation.
// String returns bounded diagnostic text with an overflow marker when needed.
func (c *Capture) String() string {
	if c.exceeded {
		return c.buffer.String() + "\n[process output exceeds 8 MiB]"
	}
	return c.buffer.String()
}

// Len returns the number of retained bytes.
func (c *Capture) Len() int { return c.buffer.Len() }

// Bytes returns retained bytes; read only after the writer finishes.
func (c *Capture) Bytes() []byte { return c.buffer.Bytes() }

// Output is exec.Cmd.Output with bounded stdout and stderr and process cleanup.
func Output(cmd *exec.Cmd) ([]byte, error) {
	out, diagnostic := NewCapture(func() { _ = Kill(cmd) }), NewCapture(func() { _ = Kill(cmd) })
	cmd.Stdout, cmd.Stderr = out, diagnostic
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		exit.Stderr = diagnostic.Bytes()
	}
	if out.exceeded || diagnostic.exceeded {
		err = errors.Join(ErrOutputLimit, err)
	}
	return out.Bytes(), err
}

// WriteByte appends one byte within the stream limit.
func (c *Capture) WriteByte(b byte) error { _, err := c.Write([]byte{b}); return err }

// Err reports overflow after the capture writer has stopped.
func (c *Capture) Err() error {
	if c.exceeded {
		return ErrOutputLimit
	}
	return nil
}
