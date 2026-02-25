package com.example.openair.ui.theme

import androidx.compose.material3.Typography
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import com.example.openair.R

// 1) Put comic_sans.ttf in: app/src/main/res/font/comic_sans.ttf
private val ComicSans = FontFamily(
    Font(R.font.comic_sans, FontWeight.Normal)
)

// 2) Apply Comic Sans to ALL Material3 text styles
val Typography = Typography().run {
    copy(
        displayLarge = displayLarge.copy(fontFamily = ComicSans),
        displayMedium = displayMedium.copy(fontFamily = ComicSans),
        displaySmall = displaySmall.copy(fontFamily = ComicSans),

        headlineLarge = headlineLarge.copy(fontFamily = ComicSans),
        headlineMedium = headlineMedium.copy(fontFamily = ComicSans),
        headlineSmall = headlineSmall.copy(fontFamily = ComicSans),

        titleLarge = titleLarge.copy(fontFamily = ComicSans),
        titleMedium = titleMedium.copy(fontFamily = ComicSans),
        titleSmall = titleSmall.copy(fontFamily = ComicSans),

        bodyLarge = bodyLarge.copy(fontFamily = ComicSans),
        bodyMedium = bodyMedium.copy(fontFamily = ComicSans),
        bodySmall = bodySmall.copy(fontFamily = ComicSans),

        labelLarge = labelLarge.copy(fontFamily = ComicSans),
        labelMedium = labelMedium.copy(fontFamily = ComicSans),
        labelSmall = labelSmall.copy(fontFamily = ComicSans),
    )
}
