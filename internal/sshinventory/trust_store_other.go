//go:build !darwin && !linux

package sshinventory

import "os"

func openPrivatePin(string) (*os.File, error) { return nil, ErrProbeUnsupported }
