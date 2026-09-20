//go:build !darwin && !linux

package sshinventory

import "os"

func openPrivatePin(*os.Root, string) (*os.File, error) { return nil, ErrProbeUnsupported }

func lockPrivateTrustRecord(*os.Root, string) (func(), error) { return nil, ErrProbeUnsupported }
