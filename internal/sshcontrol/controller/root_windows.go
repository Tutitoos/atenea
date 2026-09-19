//go:build windows

package controller

import "github.com/Tutitoos/atenea/internal/sshcontrol/localipc"

func prepareRoot(root string) error { return localipc.CheckRoot(root) }
