# Atenea SSH desktop foundation

This is a fixture-only Wails v2.15.0 shell and a separate user-owned Go
controller. It does not read SSH config, store credentials, open remote
connections or run prompts. The example addresses use the reserved
documentation network `192.0.2.0/24`.

## Local build

Use Go 1.26.7, Python 3.12 and Bun 1.4.2. From this directory:

```sh
GOTOOLCHAIN=go1.26.7 python3 scripts/restricted_wails.py test
GOTOOLCHAIN=go1.26.7 python3 scripts/restricted_wails.py build
# On macOS, from the repository root:
GOTOOLCHAIN=go1.26.7 go build -o desktop/ssh/build/bin/atenea-ssh.app/Contents/MacOS/atenea-ssh-controller ./cmd/atenea-ssh-controller
```

On macOS the controller binary belongs beside the window executable inside
`build/bin/atenea-ssh.app/Contents/MacOS/`; on Windows/Linux it belongs beside
the window executable. The window starts it on demand. Closing the window does
not stop it. A later lifecycle UI will provide explicit stop/restart.
For a manual stop, run the installed controller executable with `--stop`.

`bun run check` and `bun run build` run in `frontend/`. The browser development
preview only renders synthetic fixtures; it reports that the controller bridge
is unavailable.

## Current security boundary

The application Bind list exposes only `ControllerStatus`, which takes no
JavaScript arguments. The controller accepts only `status` and `stop` operations
after native peer authentication and protocol negotiation. The pinned Wails
source is copied into a temporary Go workspace during build. Exact upstream
source hashes are checked before the script restricts the dispatcher and the
three platform message entry points. The app references a marker available only
in that patched copy, so `go build ./...` without the script fails to compile.
The only permitted JavaScript binding is the zero-argument status call; native
window close (`Q`) and framework readiness signals remain available. Browser,
clipboard, notification, window-control, drag/resize/file-drop and obfuscated
binding messages are rejected before their framework handlers. The frontend
also sets a packaged-assets-only CSP.

The guard has direct dispatcher tests, a shared ingress-policy test and a
macOS rendered launch/close check. Direct calls from Windows and Linux
WebViews, hostile navigation probes and clean installation are still open in
issue #151. Do not ship this shell with real SSH data or credentials until
those gates pass. Development preview uses only synthetic fixtures.

The optional `probe-build` command creates a local synthetic diagnostic app
that calls framework screen and clipboard read methods directly from its
WebView. It reports only whether each call resolved, never a returned value.
The normal `build` command removes the probe flag and verifies that its label
is absent from the bundled JavaScript.

## Platform evidence

| Platform | Build | Render/launch | Installed package | Runtime dependency |
| --- | --- | --- | --- | --- |
| macOS 26.6.2 arm64 | Local restricted Wails production build passed | Local `.app` opened with CSP and displayed fixture UI; native window close left controller running and explicit stop terminated it. A diagnostic build showed direct WebView screen and clipboard reads blocked without displaying returned data | User-level copy in `~/Applications` was signed locally, verified, opened with its sibling controller, then removed from Applications; clean-system install remains open | WKWebView supplied by macOS |
| Windows 2025 CI runner, amd64 | Native Wails shell, controller build and controller tests passed | Not tested | Not tested | WebView2 runtime |
| Ubuntu 24.04 CI runner, amd64 | Native Wails shell, controller build and controller tests passed | Not tested (X11 and Wayland both pending) | Not tested | GTK3 and WebKit2GTK 4.1 |

The guarded build matrix passed on native macOS, Windows and Ubuntu runners at
`d924d5c`. A CI build
does not prove a graphical session, WebView behavior, installation or runtime
availability on a clean user machine. OS logout and sleep/resume remain
unobserved. Wails' [installation guide](https://wails.io/docs/gettingstarted/installation/)
lists platform requirements; its [build guide](https://wails.io/docs/gettingstarted/building/)
documents Ubuntu 24.04's `webkit2_41` tag. The frontend uses the bundled
Nunito font under the included SIL Open Font License; platform window chrome
and font rasterization can differ.
