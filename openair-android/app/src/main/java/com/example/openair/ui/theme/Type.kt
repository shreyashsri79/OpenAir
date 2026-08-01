package com.example.openair.ui.theme

import androidx.compose.material3.Typography
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.googlefonts.Font
import androidx.compose.ui.text.googlefonts.GoogleFont
import androidx.compose.ui.unit.sp
import com.example.openair.R

// ── Google Fonts provider ─────────────────────────────────────────────────────
val GoogleFontsProvider = GoogleFont.Provider(
    providerAuthority = "com.google.android.gms.fonts",
    providerPackage   = "com.google.android.gms",
    certificates      = R.array.com_google_android_gms_fonts_certs
)

// ── Font families ─────────────────────────────────────────────────────────────
/** Looks hand-lettered — used for section headers, app name, CTAs */
val PermanentMarkerFamily = FontFamily(
    Font(
        googleFont    = GoogleFont("Permanent Marker"),
        fontProvider  = GoogleFontsProvider,
        weight        = FontWeight.Normal
    )
)

/** Readable notebook cursive — used for body text, labels, device names */
val CaveatFamily = FontFamily(
    Font(
        googleFont    = GoogleFont("Caveat"),
        fontProvider  = GoogleFontsProvider,
        weight        = FontWeight.Normal
    ),
    Font(
        googleFont    = GoogleFont("Caveat"),
        fontProvider  = GoogleFontsProvider,
        weight        = FontWeight.Bold
    )
)

// ── Typography scale ─────────────────────────────────────────────────────────
val Typography = Typography(
    displayLarge  = TextStyle(
        fontFamily   = PermanentMarkerFamily,
        fontWeight   = FontWeight.Normal,
        fontSize     = 32.sp,
        lineHeight   = 36.sp,
        letterSpacing = 0.sp
    ),
    headlineLarge = TextStyle(
        fontFamily   = PermanentMarkerFamily,
        fontWeight   = FontWeight.Normal,
        fontSize     = 24.sp,
        lineHeight   = 28.sp,
        letterSpacing = 0.sp
    ),
    headlineMedium = TextStyle(
        fontFamily   = PermanentMarkerFamily,
        fontWeight   = FontWeight.Normal,
        fontSize     = 20.sp,
        lineHeight   = 24.sp,
        letterSpacing = 0.sp
    ),
    titleLarge = TextStyle(
        fontFamily   = CaveatFamily,
        fontWeight   = FontWeight.Bold,
        fontSize     = 22.sp,
        lineHeight   = 26.sp,
        letterSpacing = 0.sp
    ),
    titleMedium = TextStyle(
        fontFamily   = CaveatFamily,
        fontWeight   = FontWeight.Bold,
        fontSize     = 18.sp,
        lineHeight   = 22.sp,
        letterSpacing = 0.sp
    ),
    bodyLarge = TextStyle(
        fontFamily   = CaveatFamily,
        fontWeight   = FontWeight.Normal,
        fontSize     = 18.sp,
        lineHeight   = 24.sp,
        letterSpacing = 0.sp
    ),
    bodyMedium = TextStyle(
        fontFamily   = CaveatFamily,
        fontWeight   = FontWeight.Normal,
        fontSize     = 16.sp,
        lineHeight   = 22.sp,
        letterSpacing = 0.sp
    ),
    labelLarge = TextStyle(
        fontFamily   = CaveatFamily,
        fontWeight   = FontWeight.Bold,
        fontSize     = 16.sp,
        lineHeight   = 20.sp,
        letterSpacing = 0.sp
    ),
    labelMedium = TextStyle(
        fontFamily   = CaveatFamily,
        fontWeight   = FontWeight.Normal,
        fontSize     = 14.sp,
        lineHeight   = 18.sp,
        letterSpacing = 0.sp
    )
)