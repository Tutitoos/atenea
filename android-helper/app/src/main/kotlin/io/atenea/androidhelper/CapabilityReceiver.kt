package io.atenea.androidhelper

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Base64

class CapabilityReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != ACTION_CAPABILITIES) {
            resultCode = RESULT_UNSUPPORTED_ACTION
            resultData = ""
            return
        }
        resultCode = RESULT_OK
        resultData = Base64.encodeToString(
            CAPABILITY_MANIFEST.toByteArray(Charsets.UTF_8),
            Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE,
        )
    }

    companion object {
        const val ACTION_CAPABILITIES = "io.atenea.androidhelper.CAPABILITIES"
        const val RESULT_OK = 0
        const val RESULT_UNSUPPORTED_ACTION = 2
        const val CAPABILITY_MANIFEST =
            "{\"schema\":\"io.atenea.android-helper.capabilities\",\"protocol\":1," +
                "\"version\":\"0.2.0\",\"capabilities\":[" +
                "\"helper.capability_manifest\",\"helper.fixture.unicode_observation\"]}"
    }
}
