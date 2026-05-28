package com.example.openair.ui.components

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.example.openair.ui.theme.OceanDeep
import com.example.openair.ui.theme.OceanLight
import com.example.openair.ui.theme.OceanMid

/**
 * Animated "radar" ring that pulses outward when [isScanning] is true.
 * Renders three staggered rings that expand and fade in an infinite loop.
 *
 * When [isScanning] is false the component draws only the static center dot.
 *
 * @param isScanning   drives the animation on/off
 * @param size         total size of the composable (rings scale to fit)
 */
@Composable
fun ScanIndicator(
    isScanning: Boolean,
    modifier  : Modifier = Modifier,
    size      : Dp       = 80.dp
) {
    val infiniteTransition = rememberInfiniteTransition(label = "scan")

    // Three rings staggered by 600ms each
    val ring1Scale by infiniteTransition.animateFloat(
        initialValue = 0.1f, targetValue = 1f,
        animationSpec = infiniteRepeatable(
            animation  = tween(1800, delayMillis = 0, easing = LinearEasing),
            repeatMode = RepeatMode.Restart
        ), label = "ring1Scale"
    )
    val ring1Alpha by infiniteTransition.animateFloat(
        initialValue = 0.8f, targetValue = 0f,
        animationSpec = infiniteRepeatable(
            animation  = tween(1800, delayMillis = 0, easing = LinearEasing),
            repeatMode = RepeatMode.Restart
        ), label = "ring1Alpha"
    )
    val ring2Scale by infiniteTransition.animateFloat(
        initialValue = 0.1f, targetValue = 1f,
        animationSpec = infiniteRepeatable(
            animation  = tween(1800, delayMillis = 600, easing = LinearEasing),
            repeatMode = RepeatMode.Restart
        ), label = "ring2Scale"
    )
    val ring2Alpha by infiniteTransition.animateFloat(
        initialValue = 0.8f, targetValue = 0f,
        animationSpec = infiniteRepeatable(
            animation  = tween(1800, delayMillis = 600, easing = LinearEasing),
            repeatMode = RepeatMode.Restart
        ), label = "ring2Alpha"
    )
    val ring3Scale by infiniteTransition.animateFloat(
        initialValue = 0.1f, targetValue = 1f,
        animationSpec = infiniteRepeatable(
            animation  = tween(1800, delayMillis = 1200, easing = LinearEasing),
            repeatMode = RepeatMode.Restart
        ), label = "ring3Scale"
    )
    val ring3Alpha by infiniteTransition.animateFloat(
        initialValue = 0.8f, targetValue = 0f,
        animationSpec = infiniteRepeatable(
            animation  = tween(1800, delayMillis = 1200, easing = LinearEasing),
            repeatMode = RepeatMode.Restart
        ), label = "ring3Alpha"
    )

    Box(
        modifier          = modifier.size(size),
        contentAlignment  = Alignment.Center
    ) {
        Canvas(modifier = Modifier.size(size)) {
            val cx     = this.size.width  / 2f
            val cy     = this.size.height / 2f
            val maxR   = this.size.minDimension / 2f

            if (isScanning) {
                // Ring 1
                drawCircle(
                    color  = OceanLight,
                    radius = maxR * ring1Scale,
                    center = Offset(cx, cy),
                    alpha  = ring1Alpha,
                    style  = Stroke(width = 2.dp.toPx())
                )
                // Ring 2
                drawCircle(
                    color  = OceanMid,
                    radius = maxR * ring2Scale,
                    center = Offset(cx, cy),
                    alpha  = ring2Alpha,
                    style  = Stroke(width = 2.dp.toPx())
                )
                // Ring 3
                drawCircle(
                    color  = OceanDeep,
                    radius = maxR * ring3Scale,
                    center = Offset(cx, cy),
                    alpha  = ring3Alpha,
                    style  = Stroke(width = 2.dp.toPx())
                )
            }

            // Static center dot (always visible)
            drawCircle(
                color  = OceanDeep,
                radius = 6.dp.toPx(),
                center = Offset(cx, cy)
            )
            drawCircle(
                color  = androidx.compose.ui.graphics.Color(0xFF1A1A1A),
                radius = 6.dp.toPx(),
                center = Offset(cx, cy),
                style  = Stroke(width = 2.dp.toPx())
            )
        }
    }
}

@Preview(showBackground = true, backgroundColor = 0xFFF5F0E8)
@Composable
private fun ScanIndicatorPreview() {
    androidx.compose.foundation.layout.Row {
        ScanIndicator(isScanning = true,  size = 80.dp)
        ScanIndicator(isScanning = false, size = 80.dp)
    }
}
