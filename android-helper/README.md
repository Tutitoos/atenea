# Atenea Android helper

This optional Kotlin fixture validates the Android-side prerequisites for the
future helper protocol. Atenea keeps ADB as its fallback when this APK is not
installed.

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

The smoke test launches its local fixture through the fixed UIAutomation shell
command used by the host-side bridge. This avoids MIUI's cross-UID background
Activity restriction and proves that UIAutomator observes an accessibility
description and Unicode text. It does not grant Atenea extra device permissions
or replace the ADB safety boundary.
