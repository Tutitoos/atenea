package io.atenea.androidhelper

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.Until
import java.io.FileInputStream
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class HelperSmokeTest {
    @Test
    fun fixtureExposesUnicodeToUIAutomator() {
        val device = UiDevice.getInstance(InstrumentationRegistry.getInstrumentation())
        // MIUI treats the test APK as a different UID and refuses its background
        // Activity start. UiAutomation executes this fixed command as the same
        // ADB shell caller used by the host-side bridge.
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val output = FileInputStream(
            instrumentation.uiAutomation.executeShellCommand(
                "am start -W -n io.atenea.androidhelper/.MainActivity",
            ).fileDescriptor,
        ).bufferedReader().use { it.readText() }
        assertTrue(output, output.contains("Status: ok"))
        assertNotNull(device.wait(Until.findObject(By.desc("atenea unicode fixture")), 5_000))
        assertNotNull(device.findObject(By.textContains("ñ")))
    }
}
