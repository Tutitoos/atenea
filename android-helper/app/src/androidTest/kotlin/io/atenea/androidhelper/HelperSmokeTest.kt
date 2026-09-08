package io.atenea.androidhelper

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.Until
import org.junit.Assert.assertNotNull
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class HelperSmokeTest {
    @Test
    fun fixtureExposesUnicodeToUIAutomator() {
        val device = UiDevice.getInstance(InstrumentationRegistry.getInstrumentation())
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        context.startActivity(context.packageManager.getLaunchIntentForPackage(context.packageName)?.addFlags(0x10000000))
        assertNotNull(device.wait(Until.findObject(By.desc("atenea unicode fixture")), 5_000))
        assertNotNull(device.findObject(By.textContains("ñ")))
    }
}
