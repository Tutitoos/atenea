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
also sets a packaged-assets-only CSP. The guarded Windows Wails copy changes
WebView2's global permission setting from allow to deny; the fixture UI does
not request native browser permissions.

The guarded macOS WKWebView cancels navigation and new windows outside the
packaged `wails://wails/` page. The guarded Linux WebKitGTK window applies
the same restriction through its native navigation policy. Windows WebView2
uses native navigation-starting and new-window events to keep its document
at `http://wails.localhost/`. The build script checks exact source hashes and
compiles these hooks into temporary copies of Wails and go-webview2. Broader
navigation trials and native Linux desktop sessions remain necessary before
this boundary is accepted.

The per-user installation ID is written completely to a private temporary
file before being published atomically. Concurrent first launches therefore
read the same completed ID. Native endpoint ownership still allows only one
controller listener. The concurrent-ID tests cover simultaneous callers in one
process and eight independent processes. A separate process test starts a
controller, checks that a different installation cannot stop it and that an
incompatible protocol is closed before any operation, then verifies the
legitimate client still works. It also checks that a second controller cannot
take the endpoint, kills the owner, restarts it and verifies status and explicit
stop. This exercises stale endpoint recovery with synthetic state. Real macOS
and Windows graphical-session trials of simultaneous GUI activation are
recorded below; Linux, another-user sessions, logout and sleep/resume remain
separate acceptance checks.

The guard has direct dispatcher tests, a shared ingress-policy test and
rendered macOS and Windows launch checks. Diagnostic screen and clipboard
calls did not return data in the tested WebViews. The current Windows and
virtual X11 probes distinguish a rejection from a timeout: both calls timed
out after 1.5 seconds, while the permitted controller status call succeeded.
A timeout alone does not prove explicit rejection or exclude a later response.
Other hostile calls, real Linux desktop and Wayland WebView behavior, broader
navigation probes, and clean installation
remain open in issue #151. Do not ship this shell with real SSH data or
credentials until those gates pass. Development preview uses only synthetic
fixtures.

The optional `probe-build` command creates a local synthetic diagnostic app
that calls framework screen and clipboard read methods directly from its
WebView. It reports resolved, rejected, timed out or unavailable for each
call, never a returned value.
The normal `build` command removes the probe flag and verifies that its label
is absent from the bundled JavaScript.
The separate `navigation-probe-build` attempts a top-level navigation to a
loopback URL with no SSH data. A surviving fixture window after the attempt
is an observation of blocked navigation; it does not alone identify which
browser or native policy stopped it. Normal builds verify that the probe URL
is absent from the bundled JavaScript.

## Platform evidence

| Platform | Build | Render/launch | Installed package | Runtime dependency |
| --- | --- | --- | --- | --- |
| macOS 26.6.2 arm64 | Local restricted Wails production build passed | Local `.app` opened with CSP and displayed fixture UI; native window close left controller running and explicit stop terminated it. Two simultaneous windows and a reopened third window showed the fixture UI and one shared controller in a separate temporary user-state root. An earlier diagnostic build showed no resolved screen or clipboard read within 1.5 seconds, without displaying returned data; that build did not distinguish rejection from timeout. A separate navigation-probe build left the fixture UI visible after the loopback attempt in an inspected window capture | User-level copy in `~/Applications` was signed locally, verified, opened with its sibling controller, then removed from Applications; clean-system install remains open | WKWebView supplied by macOS |
| Windows 2025 CI runner, amd64 | Native Wails shell, controller build and controller tests passed | Not tested | Not tested | WebView2 runtime |
| Windows 11 Pro build 26200 test workstation, amd64 | Guarded shell and controller cross-built from macOS; transferred EXE hashes matched | Both processes started in the active user session. A later console-session trial rendered two simultaneous fixture windows and a reopened third window with one shared controller. Window-only captures showed `Controlador disponible`; the revised diagnostic showed both direct WebView screen and clipboard calls timed out after 1.5 seconds. At `3b111b9`, a separate navigation-probe build attempted a loopback page; the captured fixture window remained visible after 7 seconds | Temporary per-user EXEs launched and removed; no installer or clean-system test | WebView2 rendered the fixture window |
| Ubuntu 24.04 CI runner, amd64 | Native Wails shell, controller build and controller tests passed | Passed in virtual X11 with Xvfb. Inspected normal, bridge diagnostic and navigation-probe captures showed fixture UI and `Controlador disponible`. The navigation probe left the packaged page visible after the loopback attempt; screen/clipboard calls timed out after 1.5 seconds. A separate headless Weston session exercised two Wayland shell processes sharing one controller, window-process close and explicit controller stop. Its inspected output capture showed the synthetic device list and `Controlador disponible`. Real desktop X11 and Wayland remain open | Not tested | GTK3 and WebKit2GTK 4.1 |

At `9a0ceca`, a locally signed macOS build and sibling controller were launched
with a temporary `HOME`, so this trial did not use the normal Atenea state.
Two separate `open -n` launches rendered separate windows; inspected captures
of each contained the synthetic device list and `Controlador disponible`.
Process inspection showed two GUI processes and exactly one controller. Closing
the first window through its native close button ended only that GUI process.
A third launch rendered the same available-controller state and reused the
original controller process. Closing the remaining windows left that process
running; its explicit `--stop` terminated it. The temporary processes and state
were then removed. This observes one macOS graphical session, not another
user, Windows/Linux GUI concurrency, sleep, logout or clean-system installation.

At `c33e94f`, a guarded Windows build and sibling controller were transferred
to a separate temporary directory after SHA-256 comparison. A temporary task
ran in the logged-in console session with isolated `APPDATA`. Its report
recorded two GUI processes and exactly one controller. Three window-only
captures were inspected: the two simultaneous windows and a reopened third
window all rendered the synthetic device list and `Controlador disponible`.
Closing the first with the native window-close request left the second and the
controller running. Reopening reused the same controller process. Closing the
remaining windows left it running until explicit `--stop`, which terminated it.
The temporary task, processes and files were removed. This observes one
Windows graphical session, not another user, Linux GUI concurrency, logout,
sleep or clean-system installation.

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
an installer. A separate Windows user-session trial used a temporary
`navigation-probe-build` based on `3b111b9`. The transferred window and
controller hashes matched the cross-built binaries. Seven seconds after the
loopback navigation attempt, a window-only capture still showed the fixture
UI, the available controller and the probe's survival message. The test
processes, scheduled task and temporary directory were removed afterward.
This confirms that the window remained on its packaged page in that trial;
it does not prove which layer rejected the navigation or cover other
navigation types. On Ubuntu 24.04 CI at `a6db23f`, Xvfb launched the packaged
window and a controller in a virtual X11 session. The captured window was
inspected and visibly contained the fixture list and available controller.
The CI smoke step checks that a visible window can be captured; visual content
was verified separately by inspecting the artifact. A second Xvfb run at
`f183222` captured the diagnostic window. Both direct calls reported `sin
respuesta` after 1.5 seconds while controller status succeeded. This does not
prove explicit rejection or exclude a later response. It also does not establish
behavior in a real Linux desktop session or Wayland. At `0013f86`, another
Xvfb capture showed the fixture UI and probe survival message after the
loopback navigation attempt. The CI step captured the window; its visible
content was inspected separately. At `b31c28d`, Ubuntu CI also ran the normal
shell twice with `GDK_BACKEND=wayland` and no X11 display, under a temporary
headless Weston compositor. The lifecycle step observed both shell processes
alive and one controller process under isolated user state. After ending both
shell processes, the controller remained until its explicit stop command,
then exited. This exercises the Wayland client and controller lifecycle in a
virtual session. That process-only run did not establish visible pixels, user interaction,
navigation behavior or installation in a real Linux desktop. At `5a9235b`,
the Ubuntu CI job captured the Weston output after both shell processes had
started. The PNG was inspected separately: it showed the fixture device list,
the example-data badge and `Controlador disponible`. The CI step only checks
that one nonempty PNG was written; it does not interpret the pixels. Weston's
debug capture was enabled only for the isolated temporary compositor using
synthetic data. The image shows one foreground window, while the process check
covers the two simultaneous shells. This still does not test a real desktop,
user interaction or Wayland navigation policy. A CI build
does not prove a graphical session, WebView behavior, installation or runtime
availability on a clean user machine. OS logout and sleep/resume remain
unobserved. Wails' [installation guide](https://wails.io/docs/gettingstarted/installation/)
lists platform requirements; its [build guide](https://wails.io/docs/gettingstarted/building/)
documents Ubuntu 24.04's `webkit2_41` tag. The frontend uses the bundled
Nunito font under the included SIL Open Font License; platform window chrome
and font rasterization can differ.
