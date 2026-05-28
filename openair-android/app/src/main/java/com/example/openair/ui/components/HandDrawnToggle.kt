package com.example.openair.ui.components

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.example.openair.ui.theme.InkBlack
import com.example.openair.ui.theme.OceanDeep
import com.example.openair.ui.theme.PaperCream
import com.example.openair.ui.theme.PaperWhite

private val TRACK_WIDTH  = 68.dp
private val TRACK_HEIGHT = 34.dp
private val THUMB_PADDING = 4.dp

/**
 * Fully custom Canvas toggle switch with the marker-on-paper aesthetic.
 *
 * - Track drawn as a rounded rect with a 2dp InkBlack border
 * - Thumb slides with [animateDpAsState] (300ms ease)
 * - Track color transitions between neutral cream and OceanDeep
 * - Hard ink dot-shadow on the thumb for depth
 *
 * @param checked         current on/off state
 * @param onCheckedChange called when user taps the switch
 */
@Composable
fun HandDrawnToggle(
    checked          : Boolean,
    onCheckedChange  : () -> Unit,
    modifier         : Modifier = Modifier
) {
    val stateLabel = if (checked) "On" else "Off"

    val thumbOffsetX by animateDpAsState(
        targetValue  = if (checked) TRACK_WIDTH - TRACK_HEIGHT + THUMB_PADDING else THUMB_PADDING,
        animationSpec = tween(durationMillis = 300),
        label        = "thumbOffsetX"
    )
    val trackFill by animateColorAsState(
        targetValue  = if (checked) OceanDeep else Color(0xFFCCC5B0),
        animationSpec = tween(durationMillis = 300),
        label        = "trackFill"
    )

    Canvas(
        modifier = modifier
            .size(width = TRACK_WIDTH, height = TRACK_HEIGHT)
            .semantics {
                role             = Role.Switch
                stateDescription = stateLabel
            }
            .clickable(onClick = onCheckedChange)
    ) {
        val thumbRadius     = (size.height / 2f) - THUMB_PADDING.toPx()
        val cornerPx        = size.height / 2f
        val thumbCenterX    = thumbOffsetX.toPx() + thumbRadius
        val thumbCenterY    = size.height / 2f

        // ── Track fill ──────────────────────────────────────────────────────
        drawRoundRect(
            color        = trackFill,
            cornerRadius = CornerRadius(cornerPx)
        )

        // ── Track border ────────────────────────────────────────────────────
        drawRoundRect(
            color        = InkBlack,
            cornerRadius = CornerRadius(cornerPx),
            style        = Stroke(width = 2.dp.toPx())
        )

        // ── Thumb shadow (hard offset — no blur) ────────────────────────────
        drawCircle(
            color  = InkBlack.copy(alpha = 0.6f),
            radius = thumbRadius,
            center = Offset(thumbCenterX + 2.dp.toPx(), thumbCenterY + 2.dp.toPx())
        )

        // ── Thumb fill ──────────────────────────────────────────────────────
        drawCircle(
            color  = PaperWhite,
            radius = thumbRadius,
            center = Offset(thumbCenterX, thumbCenterY)
        )

        // ── Thumb border ────────────────────────────────────────────────────
        drawCircle(
            color  = InkBlack,
            radius = thumbRadius,
            center = Offset(thumbCenterX, thumbCenterY),
            style  = Stroke(width = 2.dp.toPx())
        )
    }
}

@Preview(showBackground = true, backgroundColor = 0xFFF5F0E8)
@Composable
private fun HandDrawnTogglePreview() {
    androidx.compose.foundation.layout.Row(
        horizontalArrangement = androidx.compose.foundation.layout.Arrangement.spacedBy(16.dp),
        modifier = Modifier.then(Modifier.size(200.dp, 80.dp))
    ) {
        HandDrawnToggle(checked = false, onCheckedChange = {})
        HandDrawnToggle(checked = true,  onCheckedChange = {})
    }
}
