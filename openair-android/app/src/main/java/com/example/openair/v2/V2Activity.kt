package com.example.openair.v2

import android.app.Activity
import android.app.KeyguardManager
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
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
                val vm: V2ViewModel = viewModel()
                CredentialGate(vm)
                V2Screen(vm)
            }
        }
    }
}

/**
 * CredentialGate runs the system's device-credential prompt when the Keystore
 * says owned access needs the user (M6, D-21 tier 1).
 *
 * It lives in the activity because only an activity can launch that intent, and
 * a view model holding one would leak it. The prompt is the platform's own —
 * PIN, pattern, password or biometric, whichever the user has set — and
 * satisfying it is what makes the Keystore release the key-encryption key for
 * the next six hours.
 */
@Composable
private fun CredentialGate(vm: V2ViewModel) {
    val context = LocalContext.current
    val needed by vm.authNeeded.collectAsStateWithLifecycle()

    val launcher = rememberLauncherForActivityResult(
        ActivityResultContracts.StartActivityForResult(),
    ) { result -> vm.onAuthenticated(result.resultCode == Activity.RESULT_OK) }

    LaunchedEffect(needed) {
        if (!needed) return@LaunchedEffect
        val km = context.getSystemService(KeyguardManager::class.java)
        val intent = km?.createConfirmDeviceCredentialIntent(
            "Unlock owned access",
            "OpenAir needs your screen lock before a paired device can act unattended.",
        )
        if (intent == null) {
            // No screen lock, or the platform declined to build a prompt. Not a
            // failure to retry: with no credential there is no tier 1 at all,
            // and the screen already says so.
            vm.onAuthenticated(false)
        } else {
            launcher.launch(intent)
        }
    }
}
