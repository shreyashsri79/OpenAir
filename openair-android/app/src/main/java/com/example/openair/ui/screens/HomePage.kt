package com.example.openair.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.example.openair.R
import com.example.openair.core.Device
import com.example.openair.core.NsdDiscoveryManager
import com.example.openair.core.OpenAirReceiverManager
import com.example.openair.core.OpenAirSender
import com.example.openair.core.PickedFile
import com.example.openair.core.SendItem
import kotlinx.coroutines.launch

// ─────────────────────────────────────────────
//  Palette  —  marker-on-paper
// ─────────────────────────────────────────────
object OpenAirColors {
    val Ink = Color(0xFF1A1A1A)
    val InkLight = Color(0xFF2D2D2D)
    val Ocean = Color(0xFF1A6EB5)
    val OceanFaint = Color(0xFFD6EAF8)
    val Paper = Color(0xFFFAF8F3)
    val PaperLine = Color(0xFFE8E4D8)
    val Scratch = Color(0xFFC8C4B8)
}

// ─────────────────────────────────────────────
//  Ink-offset shadow modifier
// ─────────────────────────────────────────────
fun Modifier.inkShadow(
    color: Color = OpenAirColors.Ink,
    offsetX: Dp = 3.dp,
    offsetY: Dp = 3.dp,
    cornerRadius: Dp = 10.dp
): Modifier = this.drawBehind {
    drawRoundRect(
        color = color,
        topLeft = Offset(offsetX.toPx(), offsetY.toPx()),
        size = size,
        cornerRadius = CornerRadius(cornerRadius.toPx())
    )
}

// ─────────────────────────────────────────────
//  Root Screen
// ─────────────────────────────────────────────
@Composable
fun OpenAirScreen() {

    val context = LocalContext.current
    val scope = rememberCoroutineScope()

    val discovery = remember { NsdDiscoveryManager(context) }

    // Discovered devices
    val wifiDevices = remember { mutableStateListOf<Device>() }

    // Selected devices
    val selectedDevices = remember { mutableStateListOf<Device>() }

    // Picked files
    val pickedFiles = remember { mutableStateListOf<PickedFile>() }

    var receiverOn by remember { mutableStateOf(false) } // for future; not used now
    var pasteText by remember { mutableStateOf("") }

    var status by remember { mutableStateOf("Ready.") }
    var progress by remember { mutableStateOf(0f) }
    var isSending by remember { mutableStateOf(false) }
    var isScanning by remember { mutableStateOf(false) }

    val receiverManager = remember { OpenAirReceiverManager(context) }

    LaunchedEffect(receiverOn) {
        if (receiverOn) {
            status = "Receiver Active (Visible as ${android.os.Build.MODEL})"
            receiverManager.startReceiver(
                onProgress = { current, total ->
                    progress = (current.toFloat() / total.toFloat()).coerceIn(0f, 1f)
                },
                onStatus = { status = it }
            )
        } else {
            receiverManager.stopReceiver()
            status = "Receiver Stopped."
            progress = 0f
        }
    }

    // Multi-file picker
    val filePicker = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.OpenMultipleDocuments()
    ) { uris ->
        val files = OpenAirSender.getPickedFiles(context, uris)
        pickedFiles.clear()
        pickedFiles.addAll(files)
        status = if (files.isEmpty()) "No files selected." else "Picked ${files.size} files."
    }

    // ─────────────────────────────────────────────
    // UI
    // ─────────────────────────────────────────────
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(OpenAirColors.Paper)
            .drawBehind { drawRuledLines() }
    ) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .verticalScroll(rememberScrollState())
                .padding(start = 22.dp, end = 22.dp, top = 28.dp, bottom = 130.dp)
        ) {

            /* ── Header ── */
            OpenAirHeader()
            Spacer(Modifier.height(18.dp))

            /* ── Receiver ── */
            ReceiverRow(isOn = receiverOn, onToggle = { receiverOn = it })
            Spacer(Modifier.height(14.dp))

            InkDivider()
            Spacer(Modifier.height(14.dp))

            /* ── Connected devices ── */
            MarkerSectionLabel("Connected Devices")
            Spacer(Modifier.height(6.dp))

            if (selectedDevices.isEmpty()) {
                Text(
                    "No device selected.",
                    fontSize = 14.sp,
                    color = OpenAirColors.Scratch,
                    fontWeight = FontWeight.SemiBold
                )
            } else {
                selectedDevices.forEach { dev ->
                    DeviceRow(
                        device = dev,
                        selected = true,
                        onClick = {
                            selectedDevices.remove(dev)
                            status = "Removed: ${dev.name}"
                        }
                    )
                }
            }

            Spacer(Modifier.height(14.dp))

            /* ── Available devices ── */
            MarkerSectionLabel("Available Devices")
            Spacer(Modifier.height(6.dp))

            Column(Modifier.padding(start = 4.dp)) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    SubSectionLabel("Search Wifi")

                    Box(
                        modifier = Modifier
                            .inkShadow(offsetX = 2.dp, offsetY = 2.dp, color = OpenAirColors.Ocean, cornerRadius = 10.dp)
                            .clip(RoundedCornerShape(10.dp))
                            .background(Color.White)
                            .border(2.dp, OpenAirColors.Ocean, RoundedCornerShape(10.dp))
                            .clickable(enabled = !isScanning && !isSending) {
                                wifiDevices.clear()
                                isScanning = true
                                status = "Scanning Wi-Fi..."

                                discovery.startScan(
                                    onStatus = { status = it },
                                    onFound = { dev ->
                                        if (wifiDevices.none { it.host == dev.host && it.port == dev.port }) {
                                            wifiDevices.add(dev)
                                        }
                                    }
                                )

                                // stop after 5s (same as previous behavior)
                                scope.launch {
                                    kotlinx.coroutines.delay(5000)
                                    discovery.stopScan()
                                    isScanning = false
                                    status = if (wifiDevices.isEmpty())
                                        "No receivers found."
                                    else
                                        "Found ${wifiDevices.size} receiver(s)."
                                }
                            }
                            .padding(horizontal = 12.dp, vertical = 8.dp),
                        contentAlignment = Alignment.Center
                    ) {
                        Text(
                            if (isScanning) "Scanning..." else "Scan",
                            fontSize = 13.sp,
                            fontWeight = FontWeight.Bold,
                            color = OpenAirColors.Ocean
                        )
                    }
                }

                Spacer(Modifier.height(6.dp))

                if (wifiDevices.isEmpty()) {
                    Text(
                        "No Wi-Fi devices yet.",
                        fontSize = 14.sp,
                        color = OpenAirColors.Scratch,
                        fontWeight = FontWeight.SemiBold
                    )
                } else {
                    wifiDevices.forEach { dev ->
                        val isSelected = selectedDevices.contains(dev)

                        DeviceRow(
                            device = dev,
                            selected = isSelected,
                            onClick = {
                                if (isSelected) {
                                    selectedDevices.remove(dev)
                                    status = "Unselected: ${dev.name}"
                                } else {
                                    selectedDevices.add(dev)
                                    status = "Selected: ${dev.name}"
                                }
                            }
                        )
                    }
                }

                // Bluetooth section placeholder (future)
                Spacer(Modifier.height(14.dp))
                SubSectionLabel("Search Bluetooth")
                Spacer(Modifier.height(6.dp))
                Text(
                    "Bluetooth coming later.",
                    fontSize = 14.sp,
                    color = OpenAirColors.Scratch,
                    fontWeight = FontWeight.SemiBold
                )
            }

            Spacer(Modifier.height(14.dp))
            InkDivider()
            Spacer(Modifier.height(14.dp))

            /* ── Send section ── */
            SendSection(
                text = pasteText,
                onText = { pasteText = it },
                files = pickedFiles,
                onAddFile = {
                    filePicker.launch(arrayOf("*/*"))
                }
            )

            Spacer(Modifier.height(14.dp))

            // Status + progress
            Text(
                "Status: $status",
                fontSize = 14.sp,
                fontWeight = FontWeight.Bold,
                color = OpenAirColors.InkLight
            )

            Spacer(Modifier.height(8.dp))

            LinearProgressIndicator(
                progress = { progress },
                modifier = Modifier
                    .fillMaxWidth()
                    .height(10.dp)
                    .clip(RoundedCornerShape(20.dp)),
                color = OpenAirColors.Ocean,
                trackColor = OpenAirColors.PaperLine
            )
        }

        /* ── Sticky send button ── */
        Box(
            modifier = Modifier
                .align(Alignment.BottomCenter)
                .fillMaxWidth()
                .background(
                    Brush.verticalGradient(
                        listOf(Color.Transparent, OpenAirColors.Paper, OpenAirColors.Paper)
                    )
                )
                .padding(horizontal = 22.dp, vertical = 14.dp)
        ) {
            SendButton(
                enabled = !isSending
            ) {
                if (selectedDevices.isEmpty()) {
                    status = "Select at least 1 device."
                    return@SendButton
                }

                val items = mutableListOf<SendItem>()

                pickedFiles.forEach { items.add(SendItem.UriFile(it)) }

                if (pasteText.isNotBlank()) {
                    val bytes = pasteText.toByteArray(Charsets.UTF_8)
                    items.add(SendItem.TextFile("openair_note.txt", bytes))
                }

                if (items.isEmpty()) {
                    status = "Pick a file or paste text."
                    return@SendButton
                }

                isSending = true
                progress = 0f

                scope.launch {
                    val ok = OpenAirSender.sendToMany(
                        context = context,
                        devices = selectedDevices.toList(),
                        items = items,
                        onStatus = { status = it },
                        onProgress = { done, total ->
                            progress = (done.toFloat() / total.toFloat()).coerceIn(0f, 1f)
                        }
                    )

                    isSending = false
                    if (ok) {
                        status = "All transfers complete."
                        progress = 1f
                    }
                }
            }
        }
    }
}

// ─────────────────────────────────────────────
//  Header
// ─────────────────────────────────────────────
@Composable
private fun OpenAirHeader() {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Box(
            modifier = Modifier
                .size(48.dp)
                .inkShadow(cornerRadius = 12.dp)
                .clip(RoundedCornerShape(12.dp))
                .background(Color.White)
                .border(2.5.dp, OpenAirColors.Ink, RoundedCornerShape(12.dp)),
            contentAlignment = Alignment.Center
        ) {
            Image(
                painter = painterResource(id = R.drawable.logo),
                contentDescription = "OpenAir logo"
            )
        }
        Spacer(Modifier.width(12.dp))
        Text(
            text = "OpenAir",
            fontSize = 30.sp,
            fontWeight = FontWeight.Black,
            color = OpenAirColors.Ink,
            letterSpacing = (-0.5).sp
        )
    }
}

// ─────────────────────────────────────────────
//  Receiver toggle row
// ─────────────────────────────────────────────
@Composable
private fun ReceiverRow(isOn: Boolean, onToggle: (Boolean) -> Unit) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically
    ) {
        Text(
            text = "Receiver",
            fontSize = 20.sp,
            fontWeight = FontWeight.ExtraBold,
            color = OpenAirColors.Ink
        )
        HandDrawnToggle(isOn = isOn, onToggle = onToggle)
    }
}

@Composable
private fun HandDrawnToggle(isOn: Boolean, onToggle: (Boolean) -> Unit) {
    val trackColor by animateColorAsState(
        if (isOn) OpenAirColors.Ocean else OpenAirColors.PaperLine,
        tween(200), label = "track"
    )
    val thumbOffset by animateDpAsState(if (isOn) 24.dp else 4.dp, tween(200), label = "thumb")

    Box(
        modifier = Modifier
            .width(52.dp)
            .height(28.dp)
            .inkShadow(offsetX = 2.dp, offsetY = 2.dp, cornerRadius = 14.dp)
            .clip(RoundedCornerShape(14.dp))
            .background(trackColor)
            .border(2.dp, OpenAirColors.Ink, RoundedCornerShape(14.dp))
            .clickable(
                indication = null,
                interactionSource = remember { MutableInteractionSource() }
            ) { onToggle(!isOn) }
    ) {
        Box(
            modifier = Modifier
                .offset(x = thumbOffset, y = 4.dp)
                .size(20.dp)
                .inkShadow(offsetX = 1.dp, offsetY = 1.dp, cornerRadius = 10.dp)
                .clip(CircleShape)
                .background(Color.White)
                .border(2.dp, OpenAirColors.Ink, CircleShape)
        )
    }
}

// ─────────────────────────────────────────────
//  Divider
// ─────────────────────────────────────────────
@Composable
private fun InkDivider() {
    Box(
        Modifier
            .fillMaxWidth()
            .height(2.5.dp)
            .background(OpenAirColors.Ink.copy(alpha = 0.75f), RoundedCornerShape(2.dp))
    )
}

// ─────────────────────────────────────────────
//  Labels
// ─────────────────────────────────────────────
@Composable
private fun MarkerSectionLabel(text: String) {
    Text(text, fontSize = 17.sp, fontWeight = FontWeight.ExtraBold, color = OpenAirColors.Ink)
}

@Composable
private fun SubSectionLabel(text: String) {
    Column {
        Text(text, fontSize = 14.sp, fontWeight = FontWeight.Bold, color = OpenAirColors.Ocean)
        Spacer(Modifier.height(2.dp))
        Box(
            Modifier
                .width(72.dp)
                .height(2.dp)
                .background(OpenAirColors.Ocean.copy(alpha = 0.35f), RoundedCornerShape(1.dp))
        )
    }
}

// ─────────────────────────────────────────────
//  Device row
// ─────────────────────────────────────────────
@Composable
private fun DeviceRow(
    device: Device,
    selected: Boolean,
    onClick: () -> Unit
) {
    Row(
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier
            .fillMaxWidth()
            .clickable { onClick() }
            .padding(horizontal = 4.dp, vertical = 5.dp)
    ) {
        val dotColor = if (selected) OpenAirColors.Ink else OpenAirColors.Ocean
        Box(
            Modifier
                .size(9.dp)
                .clip(CircleShape)
                .background(dotColor)
                .border(1.5.dp, dotColor, CircleShape)
        )
        Spacer(Modifier.width(10.dp))
        Column {
            Text(
                text = device.name,
                fontSize = 16.sp,
                fontWeight = FontWeight.SemiBold,
                color = OpenAirColors.InkLight
            )
            Text(
                text = "${device.host}:${device.port}",
                fontSize = 12.sp,
                fontWeight = FontWeight.Bold,
                color = OpenAirColors.Scratch
            )
        }
    }
}

// ─────────────────────────────────────────────
//  Send section
// ─────────────────────────────────────────────
@Composable
private fun SendSection(
    text: String,
    onText: (String) -> Unit,
    files: List<PickedFile>,
    onAddFile: () -> Unit
) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically
    ) {
        Text(
            text = "Send",
            fontSize = 20.sp,
            fontWeight = FontWeight.ExtraBold,
            color = OpenAirColors.Ink
        )
        Box(
            modifier = Modifier
                .size(32.dp)
                .inkShadow(offsetX = 2.dp, offsetY = 2.dp, color = OpenAirColors.Ocean, cornerRadius = 8.dp)
                .clip(RoundedCornerShape(8.dp))
                .background(Color.White)
                .border(2.dp, OpenAirColors.Ocean, RoundedCornerShape(8.dp))
                .clickable { onAddFile() },
            contentAlignment = Alignment.Center
        ) {
            Text(
                "+",
                fontSize = 20.sp,
                fontWeight = FontWeight.Bold,
                color = OpenAirColors.Ocean,
                modifier = Modifier.offset(y = (-1).dp)
            )
        }
    }

    Spacer(Modifier.height(10.dp))

    OutlinedTextField(
        value = text,
        onValueChange = onText,
        placeholder = {
            Text("paste text here…", color = OpenAirColors.Scratch, fontSize = 15.sp)
        },
        modifier = Modifier
            .fillMaxWidth()
            .inkShadow(offsetX = 3.dp, offsetY = 3.dp, cornerRadius = 10.dp),
        shape = RoundedCornerShape(10.dp),
        colors = OutlinedTextFieldDefaults.colors(
            unfocusedBorderColor = OpenAirColors.Ink,
            focusedBorderColor = OpenAirColors.Ocean,
            unfocusedContainerColor = Color.White,
            focusedContainerColor = Color.White,
        ),
        textStyle = LocalTextStyle.current.copy(
            fontSize = 16.sp,
            fontWeight = FontWeight.SemiBold,
            color = OpenAirColors.Ink
        ),
        minLines = 2
    )

    Spacer(Modifier.height(12.dp))

    if (files.isEmpty()) {
        Text(
            "No files selected.",
            fontSize = 14.sp,
            color = OpenAirColors.Scratch,
            fontWeight = FontWeight.SemiBold
        )
    } else {
        Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
            files.forEach { FileChip(it.name) }
        }
    }
}

@Composable
private fun FileChip(name: String) {
    Row(
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier
            .inkShadow(offsetX = 2.dp, offsetY = 2.dp, cornerRadius = 8.dp)
            .clip(RoundedCornerShape(8.dp))
            .background(Color.White)
            .border(2.dp, OpenAirColors.Ink, RoundedCornerShape(8.dp))
            .padding(horizontal = 14.dp, vertical = 8.dp)
    ) {
        Text("📄", fontSize = 13.sp)
        Spacer(Modifier.width(5.dp))
        Text(name, fontSize = 14.sp, fontWeight = FontWeight.Bold, color = OpenAirColors.Ink)
    }
}

// ─────────────────────────────────────────────
//  Send button
// ─────────────────────────────────────────────
@Composable
private fun SendButton(
    enabled: Boolean,
    onClick: () -> Unit
) {
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .inkShadow(
                offsetX = 3.dp,
                offsetY = 3.dp,
                color = OpenAirColors.Ocean,
                cornerRadius = 14.dp
            )
            .clip(RoundedCornerShape(14.dp))
            .background(if (enabled) OpenAirColors.Ink else OpenAirColors.PaperLine)
            .clickable(enabled = enabled) { onClick() }
            .padding(vertical = 15.dp),
        contentAlignment = Alignment.Center
    ) {
        Text(
            text = "Send",
            fontSize = 18.sp,
            fontWeight = FontWeight.ExtraBold,
            color = Color.White,
            letterSpacing = 1.5.sp
        )
    }
}

// ─────────────────────────────────────────────
//  Paper ruled-line background
// ─────────────────────────────────────────────
private fun DrawScope.drawRuledLines() {
    val spacing = 28.dp.toPx()
    val lineColor = Color(0xFFE8E4D8)
    var y = spacing
    while (y < size.height) {
        drawLine(lineColor, Offset(0f, y), Offset(size.width, y), 1.dp.toPx(), StrokeCap.Round)
        y += spacing
    }
}

// ─────────────────────────────────────────────
//  Preview
// ─────────────────────────────────────────────
@Preview(showBackground = true, widthDp = 360, heightDp = 780, backgroundColor = 0xFFE8E4D8)
@Composable
fun OpenAirScreenPreview() {
    OpenAirScreen()
}
