//go:build !darwin && !linux && !windows

package sshinventory

import "os"

func createPrivateTrustDirectory(string) error { return ErrProbeUnsupported }

func preparePrivateTrustFile(string, *os.File) error { return ErrProbeUnsupported }

func privateTrustDirectory(string, os.FileInfo) bool { return false }

func privateTrustFile(os.FileInfo, *os.File) bool { return false }

func openPrivatePin(*os.Root, string) (*os.File, error) { return nil, ErrProbeUnsupported }

func lockPrivateTrustRecord(*os.Root, string) (func(), error) { return nil, ErrProbeUnsupported }
