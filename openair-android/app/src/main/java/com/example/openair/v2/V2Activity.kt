package com.example.openair.v2

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import com.example.openair.ui.theme.OpenAirTheme
import com.example.openair.v2.ui.V2Screen

/**
 * V2Activity hosts the OpenAir 2.0 shell.
 *
 * It is a second entry point rather than a replacement for MainActivity: the v1
 * UI still drives the v1 Kotlin transfer code, and the two protocols cannot talk
 * to each other at all — v2 is QUIC and announces `_openair._udp`, v1 is TCP on
 * `_openair._tcp`. Keeping them apart means neither can half-work. The v1 screen
 * goes away when this one reaches parity (X5 tracks the milestones).
 */
class V2Activity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent {
            OpenAirTheme {
                V2Screen()
            }
        }
    }
}
