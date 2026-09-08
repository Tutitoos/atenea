package io.atenea.androidhelper

import android.app.Activity
import android.os.Bundle
import android.widget.TextView

class MainActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(TextView(this).apply {
            text = "Atenea helper: áéíóú ñ ✓"
            textSize = 24F
            contentDescription = "atenea unicode fixture"
        })
    }
}
