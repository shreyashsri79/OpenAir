package com.example.openair

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.*
import androidx.core.content.ContextCompat
import com.example.openair.ui.OpenAirScreen
import com.example.openair.ui.theme.OpenAirTheme

class MainActivity : ComponentActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()

        setContent {
            OpenAirTheme {
                OpenAirApp()
            }
        }
    }
}

@Composable
private fun OpenAirApp() {
    val context = androidx.compose.ui.platform.LocalContext.current

    var permissionGranted by remember { mutableStateOf(true) }

    val permissionLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.RequestPermission()
    ) { granted ->
        permissionGranted = granted
    }

    LaunchedEffect(Unit) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            val granted = ContextCompat.checkSelfPermission(
                context,
                Manifest.permission.NEARBY_WIFI_DEVICES
            ) == PackageManager.PERMISSION_GRANTED

            if (!granted) {
                permissionGranted = false
                permissionLauncher.launch(Manifest.permission.NEARBY_WIFI_DEVICES)
            }
        }
    }

    OpenAirScreen()
}
