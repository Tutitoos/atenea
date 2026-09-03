package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The retired provider must never be registered again by Headroom's defaults.
func TestOpenCodeHeadroomDisablesRetiredSerena(t *testing.T) {
	if os.Getenv("ATENEA_RETIRED_HELPER") == "1" {
		if err := launchViaHeadroom("opencode", "/tmp/opencode", "/tmp/atenea", nil, nil, func() int { port, _ := strconv.Atoi(os.Getenv("ATENEA_RETIRED_PORT")); return port }()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(97)
		}
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "headroom"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenCodeHeadroomDisablesRetiredSerena$")
	cmd.Env = append(os.Environ(), "ATENEA_RETIRED_HELPER=1", "PATH="+dir, "ATENEA_RETIRED_PORT="+strconv.Itoa(port))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "\n--no-serena\n") {
		t.Fatalf("retired provider protection missing: %s", out)
	}
}
