package com.example.openair.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.example.openair.DeviceInfo
import com.example.openair.MockDevices
import com.example.openair.ui.theme.CaveatFamily
import com.example.openair.ui.theme.InkBlack
import com.example.openair.ui.theme.InkFaded
import com.example.openair.ui.theme.InkLight
import com.example.openair.ui.theme.MarkerRed
import com.example.openair.ui.theme.OceanDeep
import com.example.openair.ui.theme.OceanLight
import com.example.openair.ui.theme.OceanMid
import com.example.openair.ui.theme.PaperCream
import com.example.openair.ui.theme.OpenAirTheme

/**
 * Single device row — stateless, receives all events as lambdas.
 *
 * Shows:  📱 icon · device name · RSSI bars · Connect / Disconnect button
 */
@Composable
fun DeviceRow(
    device            : DeviceInfo,
    onConnectClick    : (DeviceInfo) -> Unit,
    onDisconnectClick : (DeviceInfo) -> Unit,
    modifier          : Modifier = Modifier
) {
    Row(
        modifier          = modifier
            .fillMaxWidth()
            .padding(vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp)
    ) {
        // ── Device icon ──────────────────────────────────────────────────────
        Box(
            modifier = Modifier
                .size(40.dp)
                .inkOffsetShadow(offsetX = 2.dp, offsetY = 2.dp, cornerRadius = 8.dp)
                .background(
                    color = if (device.isConnected) OceanMid else PaperCream,
                    shape = RoundedCornerShape(8.dp)
                )
                .sketchBorder(strokeWidth = 2.dp, color = InkBlack, cornerRadius = 8.dp),
            contentAlignment = Alignment.Center
        ) {
            Text(text = "📱", fontSize = 18.sp)
        }

        // ── Name + signal ────────────────────────────────────────────────────
        Column(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(3.dp)) {
            Text(
                text  = device.name,
                style = TextStyle(
                    fontFamily = CaveatFamily,
                    fontWeight = androidx.compose.ui.text.font.FontWeight.Bold,
                    fontSize   = 18.sp,
                    color      = InkBlack
                )
            )
            RssiBars(rssi = device.rssi)
        }

        // ── Action button ────────────────────────────────────────────────────
        ActionMarkerButton(
            label           = if (device.isConnected) "Disconnect" else "Connect",
            onClick         = {
                if (device.isConnected) onDisconnectClick(device) else onConnectClick(device)
            },
            backgroundColor = if (device.isConnected) MarkerRed else OceanDeep,
            shadowOffsetX   = 3.dp,
            shadowOffsetY   = 3.dp,
            fontSize        = 14.sp,
            contentPadding  = androidx.compose.foundation.layout.PaddingValues(
                horizontal = 12.dp, vertical = 7.dp
            )
        )
    }
}

/** Four stacked bars indicating RSSI signal strength (0–100 → 0–4 filled). */
@Composable
private fun RssiBars(rssi: Int, modifier: Modifier = Modifier) {
    val filledBars = (rssi / 25).coerceIn(0, 4)
    Row(
        modifier             = modifier,
        horizontalArrangement = Arrangement.spacedBy(3.dp),
        verticalAlignment    = Alignment.Bottom
    ) {
        repeat(4) { idx ->
            val filled = idx < filledBars
            Box(
                modifier = Modifier
                    .width(4.dp)
                    .height((6 + idx * 4).dp)
                    .clip(RoundedCornerShape(1.dp))
                    .background(if (filled) OceanMid else PaperCream)
                    .sketchBorder(
                        strokeWidth  = 1.dp,
                        color        = if (filled) InkBlack else InkLight,
                        cornerRadius = 1.dp
                    )
            )
        }
    }
}

@Preview(showBackground = true, backgroundColor = 0xFFF5F0E8)
@Composable
private fun DeviceRowPreview() {
    OpenAirTheme {
        Column(modifier = Modifier.padding(16.dp)) {
            MockDevices.forEach { device ->
                DeviceRow(device = device, onConnectClick = {}, onDisconnectClick = {})
            }
        }
    }
}
