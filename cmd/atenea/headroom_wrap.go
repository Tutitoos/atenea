package main

// This file contains the small amount of process plumbing needed to compose
// Atenea's ephemeral MCP overlay with Headroom's client-specific wrapper.
// Headroom launches a client by name, while Atenea must resolve and launch the
// real client path. A private, self-removing shim gives Headroom the former
// without changing PATH globally or recursing back into the public wrapper.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

const (
	headroomReentryCommand = "__headroom-reentry"
	headroomAteneaConfig   = "ATENEA_HEADROOM_ATENEA_CONFIG"
	headroomReentryPath    = "ATENEA_HEADROOM_REENTRY"
	headroomClientName     = "ATENEA_HEADROOM_CLIENT"
	headroomRealClient     = "ATENEA_HEADROOM_REAL_CLIENT"
)

// launchViaHeadroom starts Headroom's own wrapper with an ephemeral client
// shim. Headroom remains responsible for proxy setup, provider configuration,
// attribution and lifecycle; the shim re-enters Atenea only to merge the MCP
// overlay immediately before the real client is exec'd.
func launchViaHeadroom(client, realBinary, ateneaBinary string, args []string, ateneaEnv []string, port int) error {
	headroomBinary, err := resolveHeadroomBinary()
	if err != nil {
		return contract.Fail(contract.FailureNotFound, "headroom no está disponible en PATH: %v", err)
	}
	if err := verifyHeadroomPort(port); err != nil {
		return err
	}
	shimDir, err := os.MkdirTemp("", "atenea-headroom-")
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "no se pudo preparar el reingreso de Headroom: %v", err)
	}
	shimPath := filepath.Join(shimDir, client)
	shim := "#!/bin/sh\n" +
		"set -eu\n" +
		"shim=\"$0\"\n" +
		"dir=\"${shim%/*}\"\n" +
		"(sleep 60; rm -f -- \"$shim\"; rmdir -- \"$dir\" 2>/dev/null || true) >/dev/null 2>&1 &\n" +
		"exec \"$" + headroomReentryPath + "\" \"" + headroomReentryCommand + "\" \"$" + headroomClientName + "\" \"$" + headroomRealClient + "\" \"$@\"\n"
	if err := os.WriteFile(shimPath, []byte(shim), 0o700); err != nil {
		_ = os.RemoveAll(shimDir)
		return contract.Fail(contract.FailureUnavailable, "no se pudo escribir el reingreso de Headroom: %v", err)
	}

	pathEnv := shimDir
	if existing := os.Getenv("PATH"); existing != "" {
		pathEnv += string(os.PathListSeparator) + existing
	}
	env := replaceEnv(os.Environ(), "PATH", pathEnv)
	env = replaceEnv(env, headroomReentryPath, ateneaBinary)
	env = replaceEnv(env, headroomClientName, client)
	env = replaceEnv(env, headroomRealClient, realBinary)
	for _, value := range ateneaEnv {
		if key, _, ok := strings.Cut(value, "="); ok {
			env = replaceEnv(env, key, value[len(key)+1:])
		}
	}
	if payload := envValue(ateneaEnv, "OPENCODE_CONFIG_CONTENT"); payload != "" {
		env = replaceEnv(env, headroomAteneaConfig, payload)
	}

	headroomArgs := []string{"wrap", client, "--port", strconv.Itoa(port)}
	switch client {
	case "claude", "codex":
		// Atenea already exposes both retrieve and code-memory servers. Avoid
		// having Headroom register a second copy of either MCP surface.
		headroomArgs = append(headroomArgs, "--no-mcp", "--code-memory", "none")
	case "opencode":
		headroomArgs = append(headroomArgs, "--no-mcp", "--no-serena")
	}
	headroomArgs = append(headroomArgs, "--")
	headroomArgs = append(headroomArgs, args...)
	argv := append([]string{"headroom"}, headroomArgs...)
	if err := syscall.Exec(headroomBinary, argv, env); err != nil {
		// Exec only returns when replacing the process failed. Do not leave a
		// private shim behind when Headroom itself could not be started.
		_ = os.RemoveAll(shimDir)
		return err
	}
	return nil
}

// verifyHeadroomPort fails closed when the requested port is already owned by
// another HTTP service. Headroom treats any listener as an existing proxy; a
// Kivgraph dashboard (or an unrelated process) would therefore make a wrapped
// client send provider traffic to the wrong service. A refused connection is
// the only state in which Headroom is allowed to attempt a bind itself.
func verifyHeadroomPort(port int) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 150*time.Millisecond)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return nil
		}
		return contract.Fail(contract.FailureUnavailable, "no se puede verificar el puerto Headroom %d: %v", port, err)
	}
	_ = conn.Close()
	// Health is a readiness check, not the 150 ms polling cadence used by the
	// supervisor. A busy proxy may need a few seconds to answer while it drains
	// a compression task; treating that transient as a wrong-port failure would
	// create a silent availability flap at every CLI launch.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "el puerto Headroom %d está ocupado y no responde como proxy: %v", port, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var health struct {
		Service string `json:"service"`
		Status  string `json:"status"`
		Ready   bool   `json:"ready"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&health); err != nil ||
		resp.StatusCode != http.StatusOK || health.Service != "headroom-proxy" || health.Status != "healthy" || !health.Ready {
		return contract.Fail(contract.FailureUnavailable, "el puerto Headroom %d está ocupado por un servicio que no es un proxy Headroom", port)
	}
	return nil
}

// resolveHeadroomBinary keeps the composition independent from launchd's
// intentionally small PATH while still honoring an explicit PATH entry first.
// The fallback locations are the two supported per-user/Homebrew installs;
// no PATH mutation is performed.
func resolveHeadroomBinary() (string, error) {
	if path, err := exec.LookPath("headroom"); err == nil {
		return filepath.Abs(path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, candidate := range []string{
		filepath.Join(home, ".local", "bin", "headroom"),
		"/opt/homebrew/bin/headroom",
		"/usr/local/bin/headroom",
	} {
		info, statErr := os.Stat(candidate)
		if statErr == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("headroom no está disponible en PATH ni en las rutas instaladas")
}

// cmdHeadroomReentry is called only by the private shim created above. It is
// deliberately not listed in the public command usage.
func cmdHeadroomReentry(args []string) error {
	if len(args) < 2 {
		return contract.Fail(contract.FailureInvalidInput, "%s requiere cliente y binario real", headroomReentryCommand)
	}
	client, realBinary := args[0], args[1]
	if os.Getenv(headroomReentryPath) == "" ||
		os.Getenv(headroomClientName) != client ||
		os.Getenv(headroomRealClient) != realBinary {
		return contract.Fail(contract.FailureUnavailable, "reingreso Headroom fuera de un shim activo")
	}
	if client == "" || realBinary == "" || !filepath.IsAbs(realBinary) {
		return contract.Fail(contract.FailureInvalidInput, "reingreso Headroom inválido")
	}
	if _, err := os.Stat(realBinary); err != nil {
		return contract.Fail(contract.FailureNotFound, "cliente real no disponible: %v", err)
	}
	launchEnv := os.Environ()
	if client == "opencode" {
		headroomPayload := os.Getenv("OPENCODE_CONFIG_CONTENT")
		ateneaPayload := os.Getenv(headroomAteneaConfig)
		merged, err := mergeOpenCodeConfig(headroomPayload, ateneaPayload)
		if err != nil {
			return contract.Fail(contract.FailureInvalidInput, "no se pudieron fusionar las configuraciones de OpenCode: %v", err)
		}
		launchEnv = replaceEnv(launchEnv, "OPENCODE_CONFIG_CONTENT", merged)
	}
	argv := append([]string{client}, args[2:]...)
	return syscall.Exec(realBinary, argv, launchEnv)
}

// mergeOpenCodeConfig deep-merges the two ephemeral payloads. Atenea owns the
// MCP map and Headroom owns provider/plugin keys. Any conflicting leaf is an
// error unless both values are byte-for-byte equivalent, preventing a wrapper
// from silently losing a server or changing a provider's routing.
func mergeOpenCodeConfig(headroomPayload, ateneaPayload string) (string, error) {
	var headroom, atenea map[string]any
	if err := decodeConfigPayload(headroomPayload, &headroom); err != nil {
		return "", fmt.Errorf("payload de Headroom: %w", err)
	}
	if err := decodeConfigPayload(ateneaPayload, &atenea); err != nil {
		return "", fmt.Errorf("payload de Atenea: %w", err)
	}
	if err := mergeJSONObjects(headroom, atenea, ""); err != nil {
		return "", err
	}
	data, err := json.Marshal(headroom)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeConfigPayload(payload string, out *map[string]any) error {
	if strings.TrimSpace(payload) == "" {
		*out = map[string]any{}
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if *out == nil {
		return errors.New("el payload no es un objeto JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("el payload contiene más de un documento JSON")
		}
		return fmt.Errorf("contenido sobrante: %w", err)
	}
	return nil
}

func mergeJSONObjects(dst, src map[string]any, path string) error {
	for key, source := range src {
		currentPath := key
		if path != "" {
			currentPath = path + "." + key
		}
		existing, ok := dst[key]
		if !ok {
			dst[key] = source
			continue
		}
		existingMap, existingOK := existing.(map[string]any)
		sourceMap, sourceOK := source.(map[string]any)
		if existingOK && sourceOK {
			if err := mergeJSONObjects(existingMap, sourceMap, currentPath); err != nil {
				return err
			}
			continue
		}
		left, _ := json.Marshal(existing)
		right, _ := json.Marshal(source)
		if !bytes.Equal(left, right) {
			return fmt.Errorf("colisión incompatible en %s", currentPath)
		}
	}
	return nil
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return env[i][len(prefix):]
		}
	}
	return ""
}
