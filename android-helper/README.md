# Atenea Android helper

This optional Kotlin fixture exposes a versioned, read-only capability
manifest and validates Android-side UIAutomator prerequisites. Atenea keeps
ADB as its fallback when this APK is absent or incompatible.

Build it with:

```sh
./gradlew :app:assembleDebug :app:assembleDebugAndroidTest
```

Install only on an explicitly authorized device, then run:

```sh
adb -s SERIAL install -r app/build/outputs/apk/debug/app-debug.apk
adb -s SERIAL install -r app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk
adb -s SERIAL shell am instrument -w io.atenea.androidhelper.test/androidx.test.runner.AndroidJUnitRunner
```

Verify the installed helper contract without opening its activity or writing
application data:

```sh
adb -s SERIAL shell am broadcast --receiver-foreground \
  -a io.atenea.androidhelper.CAPABILITIES \
  -n io.atenea.androidhelper/.CapabilityReceiver
```

The `data` value is unpadded Base64URL JSON. Version `0.2.0` advertises helper
protocol `1`. Only the ADB shell may call the receiver because the component
requires Android's privileged `DUMP` permission. An installed APK with another
schema or protocol is incompatible, not silently accepted.

The smoke tests check that manifest and launch the local fixture through the
fixed UIAutomation shell command used by the host-side bridge. This avoids
MIUI's cross-UID background Activity restriction and proves that UIAutomator observes an accessibility
description and Unicode text. It does not grant Atenea extra device permissions
or replace the ADB safety boundary. Capability negotiation proves only which
optional helper contract is available; it never proves that a requested UI
task completed.

MIUI may keep a newly installed or force-stopped package from starting its
manifest receiver. Open `io.atenea.androidhelper/.MainActivity` once after
installation if the broadcast returns no `data`; Atenea reports that state as
incompatible and `auto` falls back to ADB instead of opening the app itself.

## Semantic benchmark

After installing the fixture, measure its selector-validated action without
writing personal application data:

```sh
go run ./cmd/atenea-android-benchmark \
  --serial SERIAL --warmup 5 --repetitions 30 --semantic-helper \
  --output benchmarks/runs/android-helper-YYYY-MM-DD
```

The command stores JSON samples and a Markdown summary. A semantic sample is
successful only when the fixture is the current focused window, the exact
accessibility description is still visible and enabled, and Atenea sent the
action. `action_sent` is deliberately distinct from a verified task result.
With `--semantic-helper`, Atenea writes the reports first and then returns a
non-zero status when the semantic selector operation has failed. Use
`--require-success` to apply that same gate to every operation in a
non-semantic run.

On MIUI, an open notification shade can cover the fixture even though Android
reports that the activity was started. The benchmark makes one bounded attempt
to close that shade. If it remains, it records the repetition as `failed` with
verification `unknown`; close the system overlay on the device and rerun rather
than treating it as a selector or input success.
