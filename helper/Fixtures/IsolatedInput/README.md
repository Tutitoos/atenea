# Isolated input acceptance (macOS, opt-in)

`Fixture.swift` uses normal AppKit controls and an instrumented canvas. It does
not forward events or override `acceptsFirstMouse`. `Observer.swift` sends no
input: it samples the physical pointer, foreground app, AX focus and global
keyboard/button state, without saving keystroke contents.

Use a private directory and two synthetic applications. Stop existing fixture
processes before replacing their executables. The helper and observer need
Accessibility; the helper also needs Screen Recording and a working input tap.
Sign the helper and observer with the same designated requirement as the
installed helper to retain its grants, or grant their permissions separately.

```sh
proof_dir="$(mktemp -d "${TMPDIR:-/tmp}/atenea-input.XXXXXX")"
chmod 700 "$proof_dir"
swiftc helper/Fixtures/IsolatedInput/Fixture.swift -o "$proof_dir/fixture"
swiftc helper/Fixtures/IsolatedInput/Observer.swift -o "$proof_dir/observer"
export ATENEA_PROOF_DIR="$proof_dir"
python3 - <<'PY'
import os, pathlib, plistlib, shutil, subprocess
root = pathlib.Path(os.environ['ATENEA_PROOF_DIR'])
for role in ('target', 'decoy'):
    bundle = root / (role + '.app')
    (bundle / 'Contents/MacOS').mkdir(parents=True)
    shutil.copy2(root / 'fixture', bundle / 'Contents/MacOS/fixture')
    (bundle / 'Contents/Info.plist').write_bytes(plistlib.dumps({
        'CFBundleExecutable': 'fixture', 'CFBundlePackageType': 'APPL',
        'CFBundleIdentifier': 'org.atenea.cua-fixture.' + role,
        'CFBundleName': 'Atenea ' + role}))
    subprocess.run(['codesign', '--force', '--sign', '-', str(bundle)], check=True)
    subprocess.run(['open', '-n', '-g', str(bundle), '--args', role, str(root)], check=True)
PY
```

After signing the observer and preparing a signed release helper, use `keep`
to leave the person's foreground application alone. They can keep using their
mouse and keyboard in another application during this concurrent test:

```sh
python3 helper/Fixtures/IsolatedInput/acceptance.py \
  --state "$proof_dir" --executable /path/to/signed/helper \
  --transport helper --foreground keep --go
```

For the installed adapter use `--transport mcp --executable /path/to/atenea`;
the fixture bundle must be allowed by that user's desktop policy. Results and
screenshots are private files under `runs/`. No mutation is automatically
retried after refusal, cancellation or an uncertain result. A failed effect
or isolation sample makes the run fail; dispatch alone never passes acceptance.
`keep` checks that no CUA-marked event entered the global stream or decoy and
that CUA did not activate the target. Changes in the human's global state are
expected and reported. `target` and `decoy` are optional static comparison modes
that explicitly activate a fixture and require a separately coordinated quiet
interval; do not use them for concurrent acceptance.

The matrix covers a button, double click, logical CUA pointer, drag, scrolling,
literal text, selection shortcut and replacement text. Hover is reported
separately: a logical pointer position does not prove an app accepted hover.
Some views ignore background mouse events. Keep that failure visible; do not
add a global input fallback or change the fixture to accept first mouse.

`negative.py` checks missing/cross-process/stale frames, secure fields, changed
window geometry, another focused window and ambiguous windows. With the direct
helper transport it cancels a drag after observed dispatch and verifies that
the release arrives at the last dispatched point, without jumping to the
destination. This checks cleanup delivery, not canvas compatibility. Run it
after the positive matrix; it intentionally changes the fixture's window layout:

```sh
python3 helper/Fixtures/IsolatedInput/negative.py \
  --state "$proof_dir" --executable /path/to/signed/helper \
  --transport helper --go
```

Sampling can miss transients shorter than its recorded maximum interval. Pair
the measurements with code review confirming exclusive PID delivery and a
private event source. Human-interruption acceptance requires a separate live
action, physical interaction with the controlled app and physical Resume; never restart the helper to
simulate resuming an interrupted action.
