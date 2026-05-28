package com.example.openair.ui.components

import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.example.openair.ui.theme.InkBlack
import com.example.openair.ui.theme.PaperWhite

/**
 * Paints a hard (zero-blur) offset rectangle shadow behind the composable.
 * Deliberately visible and tactile — evokes a marker pressed on paper.
 *
 * @param offsetX horizontal shift of shadow (positive = right)
 * @param offsetY vertical shift of shadow (positive = down)
 * @param shadowColor fill color of the shadow rect (defaults to InkBlack)
 * @param cornerRadius matches the composable's own corner radius
 */
fun Modifier.inkOffsetShadow(
    offsetX     : Dp    = 4.dp,
    offsetY     : Dp    = 4.dp,
    shadowColor : Color = InkBlack,
    cornerRadius: Dp    = 8.dp
): Modifier = this.drawBehind {
    drawRoundRect(
        color        = shadowColor,
        topLeft      = Offset(offsetX.toPx(), offsetY.toPx()),
        size         = size,
        cornerRadius = CornerRadius(cornerRadius.toPx())
    )
}

/**
 * Draws a warm off-white background with horizontal ruled lines,
 * imitating a lined notebook page. Applies as a [drawBehind] so it
 * composes cleanly with other modifiers.
 *
 * @param backgroundColor warm paper fill (default PaperWhite)
 * @param lineColor        ruled-line tint (default cream)
 * @param lineSpacing      distance between each ruled line
 */
fun Modifier.ruledPaperBackground(
    backgroundColor: Color = PaperWhite,
    lineColor      : Color = Color(0xFFE0D8C5),
    lineSpacing    : Dp    = 28.dp
): Modifier = this.drawBehind {
    // Solid paper fill
    drawRect(color = backgroundColor)
    // Ruled horizontal lines
    val spacingPx = lineSpacing.toPx()
    var y = spacingPx
    while (y < size.height) {
        drawLine(
            color       = lineColor,
            start       = Offset(0f, y),
            end         = Offset(size.width, y),
            strokeWidth = 1.dp.toPx()
        )
        y += spacingPx
    }
}

/**
 * Draws a thin, ink-colored rounded-rect stroke on top of (i.e., just inside)
 * the composable's bounds. Use instead of [Modifier.border] so the stroke
 * participates in the same [drawBehind] layering model as the other marker mods.
 *
 * @param strokeWidth thickness of the border stroke
 * @param color       stroke color (defaults to InkBlack)
 * @param cornerRadius matches the composable's corner radius
 */
fun Modifier.sketchBorder(
    strokeWidth : Dp    = 2.dp,
    color       : Color = InkBlack,
    cornerRadius: Dp    = 8.dp
): Modifier = this.drawBehind {
    drawRoundRect(
        color        = color,
        size         = size,
        cornerRadius = CornerRadius(cornerRadius.toPx()),
        style        = Stroke(width = strokeWidth.toPx())
    )
}
