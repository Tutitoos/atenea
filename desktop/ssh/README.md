# Atenea SSH desktop foundation

This is a fixture-only Wails v2.15.0 shell and a separate user-owned Go
controller. It does not read SSH config, store credentials, open remote
connections or run prompts. The example addresses use the reserved
documentation network `192.0.2.0/24`.

## Local build

Use Go 1.26.7, Bun 1.4.2 and the Wails CLI v2.15.0. From this directory:

```sh
GOTOOLCHAIN=go1.26.7 go run github.com/wailsapp/wails/v2/cmd/wails@v2.15.0 build -clean
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
after native peer authentication and protocol negotiation. Wails itself still
offers framework runtime operations through its dispatcher. The complete
host-side bridge restriction, hostile navigation probes and native platform
installation checks required by issue #151 remain open. Do not ship this shell
with real SSH data or credentials until those gates pass.

## Platform evidence

| Platform | Build | Render/launch | Installed package | Runtime dependency |
| --- | --- | --- | --- | --- |
| macOS 26.6.2 arm64 | Local Wails production build passed | Local `.app` opened and displayed fixture UI; controller started and reused after window process restart | Not tested | WKWebView supplied by macOS |
| Windows 2025 CI runner, amd64 | Native Wails shell, controller build and controller tests passed | Not tested | Not tested | WebView2 runtime |
| Ubuntu 24.04 CI runner, amd64 | Native Wails shell, controller build and controller tests passed | Not tested (X11 and Wayland both pending) | Not tested | GTK3 and WebKit2GTK 4.1 |

The build matrix passed on native macOS, Windows and Ubuntu runners at
`abee0d5`. A CI build
does not prove a graphical session, WebView behavior, installation or runtime
availability on a clean user machine. OS logout and sleep/resume remain
unobserved. Wails' [installation guide](https://wails.io/docs/gettingstarted/installation/)
lists platform requirements; its [build guide](https://wails.io/docs/gettingstarted/building/)
documents Ubuntu 24.04's `webkit2_41` tag. The frontend uses the bundled
Nunito font under the included SIL Open Font License; platform window chrome
and font rasterization can differ.
