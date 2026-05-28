package com.example.openair.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.ui.text.TextStyle
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Snackbar
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TextField
import androidx.compose.material3.TextFieldDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.example.openair.IncomingRequest
import com.example.openair.MockUiState
import com.example.openair.OpenAirCallbacks
import com.example.openair.OpenAirUiState
import com.example.openair.ui.components.ActionMarkerButton
import com.example.openair.ui.components.DeviceRow
import com.example.openair.ui.components.FileChip
import com.example.openair.ui.components.HandDrawnToggle
import com.example.openair.ui.components.ScanIndicator
import com.example.openair.ui.components.TransferProgressBar
import com.example.openair.ui.components.inkOffsetShadow
import com.example.openair.ui.components.ruledPaperBackground
import com.example.openair.ui.components.sketchBorder
import com.example.openair.ui.theme.CaveatFamily
import com.example.openair.ui.theme.InkBlack
import com.example.openair.ui.theme.InkFaded
import com.example.openair.ui.theme.OceanDeep
import com.example.openair.ui.theme.OceanLight
import com.example.openair.ui.theme.OpenAirTheme
import com.example.openair.ui.theme.PaperCream
import com.example.openair.ui.theme.PaperWhite
import com.example.openair.ui.theme.PermanentMarkerFamily

// ─────────────────────────────────────────────────────────────────────────────
// Root stateless composable
// ─────────────────────────────────────────────────────────────────────────────

/**
 * 100% stateless content shell for OpenAir.
 *
 * Receives [state] and [callbacks] — never reads from a ViewModel directly.
 * All visual decisions are driven by [OpenAirUiState].
 */
@Composable
fun OpenAirContent(
    state    : OpenAirUiState,
    callbacks: OpenAirCallbacks,
    modifier : Modifier = Modifier
) {
    val snackbarHostState = remember { SnackbarHostState() }

    // Surface errors via snackbar
    LaunchedEffect(state.error) {
        state.error?.let {
            snackbarHostState.showSnackbar(it)
            callbacks.onDismissError()
        }
    }

    // ── Incoming transfer dialog ───────────────────────────────────────────────
    state.incomingRequest?.let { request ->
        IncomingTransferDialog(
            request  = request,
            onAccept = callbacks.onAcceptTransfer,
            onReject = callbacks.onRejectTransfer,
        )
    }

    Box(
        modifier = modifier
            .fillMaxSize()
            .ruledPaperBackground()
    ) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .verticalScroll(rememberScrollState())
                .statusBarsPadding()
                .padding(bottom = 100.dp) // space for sticky Send button
        ) {
            // ── Top bar ───────────────────────────────────────────────────────
            TopBar(deviceName = state.deviceName)

            Spacer(Modifier.height(16.dp))

            // ── Receiver card ─────────────────────────────────────────────────
            ReceiverCard(
                isReceiverOn  = state.isReceiverOn,
                receiverPort  = state.receiverPort,
                onToggle      = callbacks.onToggleReceiver,
                modifier      = Modifier.padding(horizontal = 16.dp)
            )

            Spacer(Modifier.height(24.dp))

            // ── Connected devices ─────────────────────────────────────────────
            if (state.connectedDevices.isNotEmpty()) {
                SectionHeader(title = "Connected Devices")
                Column(
                    modifier = Modifier.padding(horizontal = 16.dp),
                    verticalArrangement = Arrangement.spacedBy(4.dp)
                ) {
                    state.connectedDevices.forEach { device ->
                        DeviceRow(
                            device            = device,
                            onConnectClick    = callbacks.onDeviceConnect,
                            onDisconnectClick = callbacks.onDeviceDisconnect
                        )
                    }
                }
                Spacer(Modifier.height(20.dp))
            }

            // ── Available devices ─────────────────────────────────────────────
            SectionHeader(title = "Available Devices")
            Column(
                modifier = Modifier.padding(horizontal = 16.dp),
                verticalArrangement = Arrangement.spacedBy(4.dp)
            ) {
                if (state.availableDevices.isEmpty()) {
                    Text(
                        text  = "No devices nearby — tap scan \u2193",
                        style = TextStyle(fontFamily = CaveatFamily, fontSize = 16.sp, color = InkFaded)
                    )
                } else {
                    state.availableDevices.forEach { device ->
                        DeviceRow(
                            device            = device,
                            onConnectClick    = callbacks.onDeviceConnect,
                            onDisconnectClick = callbacks.onDeviceDisconnect
                        )
                    }
                }
            }

            Spacer(Modifier.height(12.dp))

            // ── Scan button + radar indicator ─────────────────────────────────
            Row(
                modifier             = Modifier
                    .padding(horizontal = 16.dp)
                    .fillMaxWidth(),
                verticalAlignment    = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(16.dp)
            ) {
                ScanIndicator(
                    isScanning = state.isScanning,
                    size       = 56.dp
                )
                ActionMarkerButton(
                    label    = if (state.isScanning) "Scanning…" else "⟳  Scan for Devices",
                    onClick  = callbacks.onScanClick,
                    modifier = Modifier.weight(1f)
                )
            }

            Spacer(Modifier.height(24.dp))

            // ── Attach & send section ─────────────────────────────────────────
            SectionHeader(title = "Attach & Send")

            // File chips (horizontally scrollable)
            if (state.attachedFiles.isNotEmpty()) {
                Row(
                    modifier = Modifier
                        .horizontalScroll(rememberScrollState())
                        .padding(horizontal = 16.dp),
                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    state.attachedFiles.forEach { file ->
                        FileChip(file = file, onRemove = callbacks.onRemoveFile)
                    }
                }
                Spacer(Modifier.height(10.dp))
            }

            // Attach-file button
            ActionMarkerButton(
                label           = "+ Attach File",
                onClick         = callbacks.onAttachFile,
                modifier        = Modifier.padding(horizontal = 16.dp),
                backgroundColor = PaperCream,
                contentColor    = InkBlack,
                shadowOffsetX   = 3.dp,
                shadowOffsetY   = 3.dp,
                fontSize        = 16.sp
            )

            Spacer(Modifier.height(12.dp))

            // Paste-text field
            TextField(
                value         = state.pastedText,
                onValueChange = callbacks.onPasteTextChange,
                modifier      = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp)
                    .inkOffsetShadow(offsetX = 3.dp, offsetY = 3.dp, cornerRadius = 8.dp)
                    .sketchBorder(strokeWidth = 2.dp, color = InkBlack, cornerRadius = 8.dp),
                placeholder   = {
                    Text(
                        text  = "Paste text to send\u2026",
                        style = TextStyle(fontFamily = CaveatFamily, fontSize = 16.sp, color = InkFaded)
                    )
                },
                textStyle     = androidx.compose.ui.text.TextStyle(
                    fontFamily = CaveatFamily,
                    fontSize   = 16.sp,
                    color      = InkBlack
                ),
                colors        = TextFieldDefaults.colors(
                    focusedContainerColor   = PaperWhite,
                    unfocusedContainerColor = PaperWhite,
                    focusedIndicatorColor   = Color.Transparent,
                    unfocusedIndicatorColor = Color.Transparent,
                    disabledIndicatorColor  = Color.Transparent
                ),
                shape         = RoundedCornerShape(8.dp),
                minLines      = 3,
                maxLines      = 6
            )

            Spacer(Modifier.height(16.dp))

            // ── Transfer progress (conditional) ───────────────────────────────
            state.activeTransfer?.let { transfer ->
                TransferProgressBar(
                    transfer = transfer,
                    modifier = Modifier
                        .padding(horizontal = 16.dp)
                        .padding(bottom = 8.dp)
                )
            }
        }

        // ── Sticky Send button (pinned to bottom) ─────────────────────────────
        Box(
            modifier = Modifier
                .align(Alignment.BottomCenter)
                .fillMaxWidth()
                .background(PaperWhite.copy(alpha = 0.95f))
                .navigationBarsPadding()
                .padding(horizontal = 16.dp, vertical = 12.dp)
        ) {
            ActionMarkerButton(
                label    = "✈  Send",
                onClick  = callbacks.onSendClick,
                modifier = Modifier.fillMaxWidth(),
                icon     = null,
                fontSize = 22.sp
            )
        }

        // ── Error snackbar ────────────────────────────────────────────────────
        SnackbarHost(
            hostState = snackbarHostState,
            modifier  = Modifier.align(Alignment.BottomCenter)
        )
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Sub-composables
// ─────────────────────────────────────────────────────────────────────────────

@Composable
private fun TopBar(
    deviceName: String,
    modifier  : Modifier = Modifier
) {
    Row(
        modifier = modifier
            .fillMaxWidth()
            .background(PaperWhite.copy(alpha = 0.98f))
            .padding(horizontal = 20.dp, vertical = 14.dp),
        verticalAlignment     = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.SpaceBetween
    ) {
        // App name — Permanent Marker for the hand-lettered logo feel
        Text(
            text  = "\u2708 OpenAir",
            style = TextStyle(fontFamily = PermanentMarkerFamily, fontSize = 26.sp, color = InkBlack)
        )
        // Device name chip
        Box(
            modifier = Modifier
                .inkOffsetShadow(offsetX = 2.dp, offsetY = 2.dp, cornerRadius = 6.dp)
                .background(OceanLight, RoundedCornerShape(6.dp))
                .sketchBorder(strokeWidth = 2.dp, color = InkBlack, cornerRadius = 6.dp)
                .padding(horizontal = 10.dp, vertical = 4.dp)
        ) {
            Text(
                text  = "\u270F $deviceName",
                style = TextStyle(
                    fontFamily = CaveatFamily,
                    fontWeight = FontWeight.Bold,
                    fontSize   = 14.sp,
                    color      = InkBlack
                )
            )
        }
    }
}

@Composable
private fun ReceiverCard(
    isReceiverOn : Boolean,
    receiverPort : Int,
    onToggle     : () -> Unit,
    modifier     : Modifier = Modifier
) {
    Box(
        modifier = modifier
            .fillMaxWidth()
            .inkOffsetShadow(offsetX = 4.dp, offsetY = 4.dp, cornerRadius = 12.dp)
            .background(
                color = if (isReceiverOn) OceanDeep else PaperCream,
                shape = RoundedCornerShape(12.dp)
            )
            .sketchBorder(strokeWidth = 2.dp, color = InkBlack, cornerRadius = 12.dp)
            .padding(16.dp)
    ) {
        Row(
            modifier              = Modifier.fillMaxWidth(),
            verticalAlignment     = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween
        ) {
            Column {
                Text(
                    text  = "Receiver",
                    style = TextStyle(
                        fontFamily = PermanentMarkerFamily,
                        fontSize   = 22.sp,
                        color      = if (isReceiverOn) PaperWhite else InkBlack
                    )
                )
                Text(
                    text     = if (isReceiverOn)
                        "Ready — listening on :$receiverPort"
                    else
                        "Off — tap to receive",
                    style    = TextStyle(
                        fontFamily = CaveatFamily,
                        fontSize   = 15.sp,
                        color      = if (isReceiverOn) OceanLight else InkFaded
                    ),
                    modifier = Modifier.padding(top = 2.dp)
                )
            }
            HandDrawnToggle(
                checked         = isReceiverOn,
                onCheckedChange = onToggle
            )
        }
    }
}



@Composable
private fun SectionHeader(
    title   : String,
    modifier: Modifier = Modifier
) {
    Row(
        modifier          = modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Text(
            text  = title,
            style = TextStyle(fontFamily = PermanentMarkerFamily, fontSize = 18.sp, color = InkBlack)
        )
        Spacer(Modifier.width(8.dp))
        Box(
            modifier = Modifier
                .weight(1f)
                .height(2.dp)
                .background(InkBlack.copy(alpha = 0.15f))
        )
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Accept / Reject dialog
// ─────────────────────────────────────────────────────────────────────────────

@Composable
private fun IncomingTransferDialog(
    request : IncomingRequest,
    onAccept: () -> Unit,
    onReject: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onReject,   // back-press / outside tap → reject
        containerColor   = PaperWhite,
        title = {
            Text(
                text  = "Incoming File",
                style = TextStyle(
                    fontFamily = PermanentMarkerFamily,
                    fontSize   = 20.sp,
                    color      = InkBlack
                )
            )
        },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                Text(
                    text  = request.fileName,
                    style = TextStyle(
                        fontFamily = CaveatFamily,
                        fontSize   = 18.sp,
                        fontWeight = FontWeight.Bold,
                        color      = InkBlack
                    )
                )
                Text(
                    text  = request.sizeLabel,
                    style = TextStyle(
                        fontFamily = CaveatFamily,
                        fontSize   = 15.sp,
                        color      = InkFaded
                    )
                )
                Spacer(Modifier.height(4.dp))
                Text(
                    text  = "Accept this transfer?",
                    style = TextStyle(
                        fontFamily = CaveatFamily,
                        fontSize   = 15.sp,
                        color      = InkBlack
                    )
                )
            }
        },
        confirmButton = {
            Button(
                onClick = onAccept,
                colors  = ButtonDefaults.buttonColors(containerColor = OceanDeep)
            ) {
                Text(
                    text  = "Accept",
                    style = TextStyle(
                        fontFamily = PermanentMarkerFamily,
                        fontSize   = 15.sp,
                        color      = PaperWhite
                    )
                )
            }
        },
        dismissButton = {
            TextButton(onClick = onReject) {
                Text(
                    text  = "Reject",
                    style = TextStyle(
                        fontFamily = CaveatFamily,
                        fontSize   = 15.sp,
                        color      = InkFaded
                    )
                )
            }
        }
    )
}

// ─────────────────────────────────────────────────────────────────────────────
// @Preview (fully populated with mock data — no ViewModel needed)
// ─────────────────────────────────────────────────────────────────────────────

@Preview(
    name           = "OpenAir — Full Mock",
    showBackground = true,
    showSystemUi   = true,
    device         = "spec:width=411dp,height=891dp,dpi=420"
)
@Composable
fun OpenAirContentPreview() {
    OpenAirTheme {
        OpenAirContent(
            state     = MockUiState,
            callbacks = OpenAirCallbacks() // all no-ops
        )
    }
}

@Preview(
    name           = "OpenAir — Empty / Idle State",
    showBackground = true,
    backgroundColor = 0xFFF5F0E8
)
@Composable
fun OpenAirContentIdlePreview() {
    OpenAirTheme {
        OpenAirContent(
            state     = OpenAirUiState(deviceName = "My Phone"),
            callbacks = OpenAirCallbacks()
        )
    }
}

@Preview(
    name           = "OpenAir — Receiving Mode",
    showBackground = true,
    backgroundColor = 0xFFF5F0E8
)
@Composable
fun OpenAirContentReceivingPreview() {
    OpenAirTheme {
        OpenAirContent(
            state = OpenAirUiState(
                deviceName   = "Pixel 9 Pro",
                isReceiverOn = true,
                activeTransfer = com.example.openair.TransferState(
                    "archive.zip", 0.30f, com.example.openair.TransferDirection.RECEIVING
                )
            ),
            callbacks = OpenAirCallbacks()
        )
    }
}
