package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/sshcontrol/controller"
	"github.com/Tutitoos/atenea/internal/sshcontrol/handshake"
	"github.com/Tutitoos/atenea/internal/sshcontrol/localipc"
)

// App exposes only fixture lifecycle data. Wails framework runtime methods
// require a separate host-side bridge audit before this becomes production UI.
type App struct{ mu sync.Mutex }

type ControllerView struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// ControllerStatus starts the sibling process on demand. It never takes a path
// or command from JavaScript and never dispatches a remote operation.
func (a *App) ControllerStatus() ControllerView {
	a.mu.Lock()
	defer a.mu.Unlock()
	root, err := controller.Root()
	if err != nil {
		return ControllerView{"error", "No se encuentra el directorio del usuario"}
	}
	if err := localipc.ValidateEndpoint(root); errors.Is(err, localipc.ErrEndpointTooLong) {
		return ControllerView{"error", "La ruta del usuario es demasiado larga para el socket local"}
	}
	id, err := controller.InstallationID(root)
	if err != nil {
		return ControllerView{"error", "No se puede abrir el estado local"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if status, err := controller.Call(ctx, root, id, "status"); err == nil {
		return ControllerView{status.State, "Controlador disponible"}
	} else if errors.Is(err, handshake.ErrVersion) || errors.Is(err, handshake.ErrPeer) {
		return ControllerView{"incompatible", "Versión o instalación del controlador incompatible"}
	} else if errors.Is(err, localipc.ErrPeer) || errors.Is(err, localipc.ErrPrivateRoot) {
		return ControllerView{"error", "No se pudo verificar la identidad del controlador"}
	}
	executable, err := os.Executable()
	if err != nil {
		return ControllerView{"error", "No se encuentra la instalación"}
	}
	name := "atenea-ssh-controller"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	command := exec.Command(filepath.Join(filepath.Dir(executable), name), "--root", root)
	if err := command.Start(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ControllerView{"missing", "Falta el controlador en la instalación"}
		}
		return ControllerView{"error", "No se pudo iniciar el controlador"}
	}
	_ = command.Process.Release()
	probe := time.NewTicker(100 * time.Millisecond)
	defer probe.Stop()
	for {
		select {
		case <-ctx.Done():
			return ControllerView{"error", "El controlador no respondió a tiempo"}
		case <-probe.C:
			if status, err := controller.Call(ctx, root, id, "status"); err == nil {
				return ControllerView{status.State, "Controlador disponible"}
			}
		}
	}
}
