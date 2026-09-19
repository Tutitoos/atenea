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
# To build the guarded Windows amd64 shell from a macOS host:
ATENEA_WAILS_PLATFORM=windows/amd64 GOTOOLCHAIN=go1.26.7 python3 scripts/restricted_wails.py build
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

The guarded macOS WKWebView cancels navigation and new windows outside the
packaged `wails://wails/` page. The guarded Linux WebKitGTK window applies
the same restriction through its native navigation policy. Windows WebView2
uses native navigation-starting and new-window events to keep its document
at `http://wails.localhost/`. The build script checks exact source hashes and
compiles these hooks into temporary copies of Wails and go-webview2. Hostile
navigation trials on each platform remain necessary before this boundary is
accepted.

The per-user installation ID is written completely to a private temporary
file before being published atomically. Concurrent first launches therefore
read the same completed ID. Native endpoint ownership still allows only one
controller listener. The concurrent-ID test covers simultaneous callers in
one process. A separate process test starts a controller, checks that a second
controller cannot take its endpoint, kills the owner, restarts it and verifies
status and explicit stop. This exercises stale endpoint recovery with synthetic
state; simultaneous GUI activation, logout and sleep/resume remain separate
acceptance checks.

The guard has direct dispatcher tests, a shared ingress-policy test and
rendered macOS and Windows launch checks. Diagnostic screen and clipboard
calls did not return data in the tested WebViews. The current Windows and
virtual X11 probes distinguish a rejection from a timeout: both calls timed
out after 1.5 seconds, while the permitted controller status call succeeded.
A timeout alone does not prove explicit rejection or exclude a later response.
Other hostile calls, real Linux desktop and Wayland WebView behavior, hostile
navigation probes and clean installation
remain open in issue #151. Do not ship this shell with real SSH data or
credentials until those gates pass. Development preview uses only synthetic
fixtures.

The optional `probe-build` command creates a local synthetic diagnostic app
that calls framework screen and clipboard read methods directly from its
WebView. It reports resolved, rejected, timed out or unavailable for each
call, never a returned value.
The normal `build` command removes the probe flag and verifies that its label
is absent from the bundled JavaScript.

## Platform evidence

| Platform | Build | Render/launch | Installed package | Runtime dependency |
| --- | --- | --- | --- | --- |
| macOS 26.6.2 arm64 | Local restricted Wails production build passed | Local `.app` opened with CSP and displayed fixture UI; native window close left controller running and explicit stop terminated it. An earlier diagnostic build showed no resolved screen or clipboard read within 1.5 seconds, without displaying returned data; that build did not distinguish rejection from timeout | User-level copy in `~/Applications` was signed locally, verified, opened with its sibling controller, then removed from Applications; clean-system install remains open | WKWebView supplied by macOS |
| Windows 2025 CI runner, amd64 | Native Wails shell, controller build and controller tests passed | Not tested | Not tested | WebView2 runtime |
| Windows 11 Pro build 26200 test workstation, amd64 | Guarded shell and controller cross-built from macOS; transferred EXE hashes matched | Both processes started in the active user session. Window-only captures showed fixture UI and `Controlador disponible`; the revised diagnostic showed both direct WebView screen and clipboard calls timed out after 1.5 seconds | Temporary per-user EXEs launched and removed; no installer or clean-system test | WebView2 rendered the fixture window |
| Ubuntu 24.04 CI runner, amd64 | Native Wails shell, controller build and controller tests passed | Passed in virtual X11 with Xvfb. Inspected normal and diagnostic window captures showed fixture UI, `Controlador disponible`, and screen/clipboard calls timing out after 1.5 seconds; real desktop X11 and Wayland remain open | Not tested | GTK3 and WebKit2GTK 4.1 |

The guarded build matrix passed on native macOS, Windows and Ubuntu runners at
`2de550f`. The Windows workstation trial used a temporary interactive scheduled task to
launch the two verified EXEs in the active user session. The capture selected
only the Atenea window. A second trial used `probe-build`; its original
`Puente bloqueado` label conflated rejection with a timeout. The revised probe
showed `Pantalla: sin respuesta · Portapapeles: sin respuesta` after direct
framework read calls from the Windows WebView. It never displays or records
returned values. This is observed non-response within 1.5 seconds, not proof
of an explicit rejection.
Both preview processes, the two temporary tasks and the temporary directory
were removed after each trial. The normal build was restored afterward. This
does not verify every framework operation, remote SSH, credential storage or
an installer. On Ubuntu 24.04 CI at `a6db23f`, Xvfb launched the packaged
window and a controller in a virtual X11 session. The captured window was
inspected and visibly contained the fixture list and available controller.
The CI smoke step checks that a visible window can be captured; visual content
was verified separately by inspecting the artifact. A second Xvfb run at
`f183222` captured the diagnostic window. Both direct calls reported `sin
respuesta` after 1.5 seconds while controller status succeeded. This does not
prove explicit rejection or exclude a later response. It also does not establish
behavior in a real Linux desktop session or Wayland. A CI build
does not prove a graphical session, WebView behavior, installation or runtime
availability on a clean user machine. OS logout and sleep/resume remain
unobserved. Wails' [installation guide](https://wails.io/docs/gettingstarted/installation/)
lists platform requirements; its [build guide](https://wails.io/docs/gettingstarted/building/)
documents Ubuntu 24.04's `webkit2_41` tag. The frontend uses the bundled
Nunito font under the included SIL Open Font License; platform window chrome
and font rasterization can differ.
