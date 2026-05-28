package com.example.openair.ui.theme

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.staticCompositionLocalOf

// ── Custom color holder (bypasses M3 dynamic color system) ───────────────────
data class OpenAirColors(
    val paperWhite : androidx.compose.ui.graphics.Color = PaperWhite,
    val paperCream : androidx.compose.ui.graphics.Color = PaperCream,
    val paperDeep  : androidx.compose.ui.graphics.Color = PaperDeep,
    val inkBlack   : androidx.compose.ui.graphics.Color = InkBlack,
    val inkFaded   : androidx.compose.ui.graphics.Color = InkFaded,
    val inkLight   : androidx.compose.ui.graphics.Color = InkLight,
    val oceanDeep  : androidx.compose.ui.graphics.Color = OceanDeep,
    val oceanMid   : androidx.compose.ui.graphics.Color = OceanMid,
    val oceanLight : androidx.compose.ui.graphics.Color = OceanLight,
    val markerRed  : androidx.compose.ui.graphics.Color = MarkerRed,
    val markerGreen: androidx.compose.ui.graphics.Color = MarkerGreen,
    val markerAmber: androidx.compose.ui.graphics.Color = MarkerAmber
)

val LocalOpenAirColors = staticCompositionLocalOf { OpenAirColors() }

// ── M3 color scheme wired to Paper/Ink tokens ─────────────────────────────────
private val PaperColorScheme = lightColorScheme(
    primary         = OceanDeep,
    onPrimary       = PaperWhite,
    primaryContainer = OceanLight,
    onPrimaryContainer = OceanDeep,
    secondary       = OceanMid,
    onSecondary     = PaperWhite,
    background      = PaperWhite,
    onBackground    = InkBlack,
    surface         = PaperWhite,
    onSurface       = InkBlack,
    surfaceVariant  = PaperCream,
    onSurfaceVariant = InkFaded,
    outline         = InkLight,
    error           = MarkerRed,
    onError         = PaperWhite
)

@Composable
fun OpenAirTheme(
    // Dynamic color is intentionally disabled — the Paper aesthetic must be consistent
    content: @Composable () -> Unit
) {
    CompositionLocalProvider(LocalOpenAirColors provides OpenAirColors()) {
        MaterialTheme(
            colorScheme = PaperColorScheme,
            typography  = Typography,
            content     = content
        )
    }
}