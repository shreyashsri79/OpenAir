package com.example.openair.ui.components

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.example.openair.TransferDirection
import com.example.openair.TransferState
import com.example.openair.ui.theme.CaveatFamily
import com.example.openair.ui.theme.InkBlack
import com.example.openair.ui.theme.InkFaded
import com.example.openair.ui.theme.MarkerGreen
import com.example.openair.ui.theme.OceanDeep
import com.example.openair.ui.theme.OceanLight
import com.example.openair.ui.theme.OpenAirTheme
import com.example.openair.ui.theme.PaperCream

/**
 * Skeuomorphic progress bar for an active file transfer.
 *
 * Draws entirely on a [Canvas]:
 * - Outer ink border (sketch style)
 * - Filled track (OceanDeep for SENDING, MarkerGreen for RECEIVING)
 * - Percentage label via Text composable above the bar
 *
 * @param transfer the active [TransferState] describing direction, file name, and progress
 */
@Composable
fun TransferProgressBar(
    transfer: TransferState,
    modifier: Modifier = Modifier
) {
    val directionArrow = if (transfer.direction == TransferDirection.SENDING) "↑" else "↓"
    val directionLabel = if (transfer.direction == TransferDirection.SENDING) "Sending" else "Receiving"
    val fillColor      = if (transfer.direction == TransferDirection.SENDING) OceanDeep else MarkerGreen
    // ── Animate raw progress → smooth float ──────────────────────────────────
    // The engine emits progress in bursty LAN chunks; animateFloatAsState smooths
    // them into a 300 ms linear glide so the bar never jumps.
    val animatedProgress by animateFloatAsState(
        targetValue  = transfer.progress.coerceIn(0f, 1f),
        animationSpec = tween(durationMillis = 300, easing = LinearEasing),
        label        = "transferProgress"
    )
    val percent = (animatedProgress * 100).toInt()

    Column(modifier = modifier.fillMaxWidth()) {
        // ── Status label ──────────────────────────────────────────────────────
        Text(
            text     = "$directionArrow $directionLabel \"${transfer.fileName}\" — $percent%",
            style    = androidx.compose.ui.text.TextStyle(
                fontFamily = CaveatFamily,
                fontWeight = FontWeight.Bold,
                fontSize   = 16.sp,
                color      = InkFaded
            ),
            modifier = Modifier.padding(bottom = 6.dp)
        )

        // ── Engine status subtitle (RTT, bandwidth, etc.) ─────────────────────
        if (transfer.transferStatus.isNotBlank()) {
            Text(
                text     = transfer.transferStatus,
                style    = androidx.compose.ui.text.TextStyle(
                    fontFamily = CaveatFamily,
                    fontSize   = 13.sp,
                    color      = InkFaded.copy(alpha = 0.75f)
                ),
                modifier = Modifier.padding(bottom = 6.dp)
            )
        }

        // ── Canvas bar ────────────────────────────────────────────────────────
        Canvas(
            modifier = Modifier
                .fillMaxWidth()
                .height(24.dp)
        ) {
            val barHeight     = size.height
            val barWidth      = size.width
            val cornerPx      = (barHeight / 2f)
            val progressWidth = barWidth * animatedProgress  // ← animated value

            // Background track
            drawRoundRect(
                color        = PaperCream,
                size         = Size(barWidth, barHeight),
                cornerRadius = CornerRadius(cornerPx)
            )

            // Filled progress track
            if (progressWidth > 0f) {
                drawRoundRect(
                    color        = fillColor,
                    size         = Size(progressWidth, barHeight),
                    cornerRadius = CornerRadius(cornerPx)
                )
            }

            // Hatching marks every ~20px for "notebook ruler" feel
            val markSpacing = 20.dp.toPx()
            var mx = markSpacing
            while (mx < progressWidth - 4.dp.toPx()) {
                drawLine(
                    color       = fillColor.copy(alpha = 0.4f),
                    start       = Offset(mx, 4.dp.toPx()),
                    end         = Offset(mx, barHeight - 4.dp.toPx()),
                    strokeWidth = 1.dp.toPx()
                )
                mx += markSpacing
            }

            // Outer border (ink sketch)
            drawRoundRect(
                color        = InkBlack,
                size         = Size(barWidth, barHeight),
                cornerRadius = CornerRadius(cornerPx),
                style        = Stroke(width = 2.dp.toPx())
            )

            // Hard shadow
            drawRoundRect(
                color        = InkBlack.copy(alpha = 0.6f),
                topLeft      = Offset(3.dp.toPx(), 3.dp.toPx()),
                size         = Size(barWidth, barHeight),
                cornerRadius = CornerRadius(cornerPx)
            )
        }
    }
}

@Preview(showBackground = true, backgroundColor = 0xFFF5F0E8)
@Composable
private fun TransferProgressBarPreview() {
    OpenAirTheme {
        Column(modifier = Modifier.padding(16.dp)) {
            TransferProgressBar(
                transfer = TransferState(
                    fileName       = "sunset.jpg",
                    progress       = 0.62f,
                    direction      = TransferDirection.SENDING,
                    transferStatus = "RTT 4 ms · 120 MB/s · 3 workers"
                ),
                modifier = Modifier.padding(bottom = 16.dp)
            )
            TransferProgressBar(
                transfer = TransferState(
                    fileName  = "archive.zip",
                    progress  = 0.30f,
                    direction = TransferDirection.RECEIVING
                )
            )
        }
    }
}
