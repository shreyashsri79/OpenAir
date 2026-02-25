//package com.example.openair.ui.screens
//
//
//
//import android.Manifest
//import android.content.Context
//import android.content.pm.PackageManager
//import android.net.Uri
//import android.net.nsd.NsdManager
//import android.net.nsd.NsdServiceInfo
//import android.os.Build
//import android.os.Bundle
//import android.provider.OpenableColumns
//import androidx.activity.ComponentActivity
//import androidx.activity.compose.rememberLauncherForActivityResult
//import androidx.activity.compose.setContent
//import androidx.activity.enableEdgeToEdge
//import androidx.activity.result.contract.ActivityResultContracts
//import androidx.compose.animation.*
//import androidx.compose.animation.core.*
//import androidx.compose.foundation.background
//import androidx.compose.foundation.border
//import androidx.compose.foundation.clickable
//import androidx.compose.foundation.layout.*
//import androidx.compose.foundation.lazy.LazyColumn
//import androidx.compose.foundation.lazy.items
//import androidx.compose.foundation.shape.CircleShape
//import androidx.compose.foundation.shape.RoundedCornerShape
//import androidx.compose.material.icons.Icons
//import androidx.compose.material.icons.rounded.*
//import androidx.compose.material3.*
//import androidx.compose.runtime.*
//import androidx.compose.ui.Alignment
//import androidx.compose.ui.Modifier
//import androidx.compose.ui.draw.clip
//import androidx.compose.ui.draw.drawBehind
//import androidx.compose.ui.geometry.Offset
//import androidx.compose.ui.graphics.Brush
//import androidx.compose.ui.graphics.Color
//import androidx.compose.ui.graphics.StrokeCap
//import androidx.compose.ui.graphics.drawscope.Stroke
//import androidx.compose.ui.platform.LocalContext
//import androidx.compose.ui.text.font.Font
//import androidx.compose.ui.text.font.FontFamily
//import androidx.compose.ui.text.font.FontWeight
//import androidx.compose.ui.text.style.TextOverflow
//import androidx.compose.ui.tooling.preview.Preview
//import androidx.compose.ui.unit.dp
//import androidx.compose.ui.unit.sp
//import androidx.core.content.ContextCompat
//import com.example.openair.ui.theme.OpenAirTheme
//import kotlinx.coroutines.Dispatchers
//import kotlinx.coroutines.launch
//import kotlinx.coroutines.withContext
//import java.io.BufferedInputStream
//import java.io.BufferedOutputStream
//import java.io.BufferedReader
//import java.io.InputStreamReader
//import java.net.Inet4Address
//import java.net.InetAddress
//import java.net.Socket
//import java.security.MessageDigest
//
//// ─── Constants ───────────────────────────────────────────────────────────────
//private const val SERVICE_TYPE = "_openair._tcp."
//private const val DEFAULT_PORT = 8089
//
//// ─── Color Palette ───────────────────────────────────────────────────────────
//private val Ink900     = Color(0xFF0D0F12)
//private val Ink800     = Color(0xFF141720)
//private val Ink700     = Color(0xFF1C2030)
//private val Ink600     = Color(0xFF252A3A)
//private val Ink400     = Color(0xFF4A5270)
//private val Ink300     = Color(0xFF6B748F)
//private val Ink100     = Color(0xFFB8C0D8)
//private val Cyan400    = Color(0xFF22D3EE)
//private val Cyan500    = Color(0xFF06B6D4)
//private val Cyan900    = Color(0xFF083344)
//private val Green400   = Color(0xFF4ADE80)
//private val Amber400   = Color(0xFFFBBF24)
//private val Red400     = Color(0xFFF87171)
//
//// ─── Data classes ────────────────────────────────────────────────────────────
//data class ReceiverDevice(val name: String, val host: InetAddress, val port: Int)
//data class PickedFile(val uri: Uri, val name: String, val size: Long)
//
//// ─── Activity ────────────────────────────────────────────────────────────────
//class MainActivity : ComponentActivity() {
//    override fun onCreate(savedInstanceState: Bundle?) {
//        super.onCreate(savedInstanceState)
//        enableEdgeToEdge()
//        setContent { OpenAirTheme { OpenAirSenderScreen() } }
//    }
//}
//
//// ─── Root Screen ─────────────────────────────────────────────────────────────
//@Composable
//fun OpenAirSenderScreen() {
//    val context = LocalContext.current
//    val scope   = rememberCoroutineScope()
//
//    val permissionLauncher = rememberLauncherForActivityResult(
//        ActivityResultContracts.RequestPermission()
//    ) {}
//
//    var receivers        by remember { mutableStateOf<List<ReceiverDevice>>(emptyList()) }
//    var selectedReceiver by remember { mutableStateOf<ReceiverDevice?>(null) }
//    var picked           by remember { mutableStateOf<PickedFile?>(null) }
//    var status           by remember { mutableStateOf<StatusState>(StatusState.Idle) }
//    var progress         by remember { mutableStateOf(0f) }
//    var isSending        by remember { mutableStateOf(false) }
//    var isScanning       by remember { mutableStateOf(false) }
//
//    val picker = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
//        if (uri == null) return@rememberLauncherForActivityResult
//        val meta = getFileMeta(context, uri)
//        if (meta == null) { status = StatusState.Error("Failed to read file metadata."); return@rememberLauncherForActivityResult }
//        picked = PickedFile(uri, meta.first, meta.second)
//        status = StatusState.Info("Ready to send  ${meta.first}")
//        progress = 0f
//    }
//
//    Box(
//        modifier = Modifier
//            .fillMaxSize()
//            .background(Ink900)
//    ) {
//        // Subtle gradient orb top-right
//        Box(
//            modifier = Modifier
//                .size(320.dp)
//                .offset(x = 160.dp, y = (-60).dp)
//                .background(
//                    Brush.radialGradient(
//                        colors = listOf(Cyan900.copy(alpha = 0.45f), Color.Transparent)
//                    )
//                )
//        )
//
//        Column(
//            modifier = Modifier
//                .fillMaxSize()
//                .systemBarsPadding()
//                .padding(horizontal = 20.dp),
//            verticalArrangement = Arrangement.spacedBy(0.dp)
//        ) {
//            // ── Header ──────────────────────────────────────────────────────
//            Spacer(Modifier.height(24.dp))
//            AppHeader()
//            Spacer(Modifier.height(28.dp))
//
//            // ── Action row ──────────────────────────────────────────────────
//            Row(
//                modifier = Modifier.fillMaxWidth(),
//                horizontalArrangement = Arrangement.spacedBy(12.dp)
//            ) {
//                ScanButton(
//                    isScanning  = isScanning,
//                    enabled     = !isScanning && !isSending,
//                    modifier    = Modifier.weight(1f),
//                    onClick = {
//                        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
//                            val granted = ContextCompat.checkSelfPermission(
//                                context, Manifest.permission.NEARBY_WIFI_DEVICES
//                            ) == PackageManager.PERMISSION_GRANTED
//                            if (!granted) {
//                                status = StatusState.Warning("Grant Nearby Wi-Fi permission.")
//                                permissionLauncher.launch(Manifest.permission.NEARBY_WIFI_DEVICES)
//                                return@ScanButton
//                            }
//                        }
//                        status = StatusState.Info("Scanning…")
//                        isScanning = true; selectedReceiver = null; receivers = emptyList()
//                        discoverReceivers(
//                            context,
//                            onFound  = { dev ->
//                                receivers = (receivers + dev)
//                                    .distinctBy { it.host.hostAddress + ":" + it.port }
//                                    .sortedBy { it.name }
//                            },
//                            onStatus = { status = StatusState.Info(it) },
//                            onDone   = { isScanning = false }
//                        )
//                    }
//                )
//                PickFileButton(
//                    hasPick  = picked != null,
//                    enabled  = !isSending,
//                    modifier = Modifier.weight(1f),
//                    onClick  = { picker.launch(arrayOf("*/*")) }
//                )
//            }
//
//            Spacer(Modifier.height(24.dp))
//
//            // ── Receiver list ────────────────────────────────────────────────
//            SectionLabel(
//                icon  = Icons.Rounded.Call,
//                label = "Nearby Receivers",
//                badge = if (receivers.isNotEmpty()) receivers.size.toString() else null
//            )
//            Spacer(Modifier.height(10.dp))
//
//            if (receivers.isEmpty()) {
//                EmptyReceiversCard(isScanning)
//            } else {
//                LazyColumn(
//                    modifier              = Modifier
//                        .fillMaxWidth()
//                        .heightIn(max = 230.dp),
//                    verticalArrangement   = Arrangement.spacedBy(8.dp)
//                ) {
//                    items(receivers) { dev ->
//                        ReceiverCard(
//                            device     = dev,
//                            isSelected = selectedReceiver == dev,
//                            enabled    = !isSending,
//                            onClick    = {
//                                selectedReceiver = dev
//                                status = StatusState.Info("Selected  ${dev.name}")
//                            }
//                        )
//                    }
//                }
//            }
//
//            Spacer(Modifier.height(20.dp))
//
//            // ── File card ────────────────────────────────────────────────────
//            SectionLabel(icon = Icons.Rounded.Add, label = "Selected File")
//            Spacer(Modifier.height(10.dp))
//            FileCard(picked)
//
//            Spacer(Modifier.height(20.dp))
//
//            // ── Progress & Status ────────────────────────────────────────────
//            TransferProgress(progress = progress, isSending = isSending)
//            Spacer(Modifier.height(12.dp))
//            StatusRow(state = status)
//
//            Spacer(Modifier.weight(1f))
//
//            // ── Send button ──────────────────────────────────────────────────
//            SendButton(
//                isSending = isSending,
//                enabled   = !isSending,
//                onClick = {
//                    val receiver = selectedReceiver
//                    val file     = picked
//                    if (receiver == null) { status = StatusState.Warning("Select a receiver first."); return@SendButton }
//                    if (file    == null)  { status = StatusState.Warning("Pick a file first.");      return@SendButton }
//
//                    isSending = true
//                    status    = StatusState.Info("Computing SHA-256…")
//                    progress  = 0f
//
//                    scope.launch {
//                        val ok = sendFileToReceiver(
//                            context    = context,
//                            receiver   = receiver,
//                            file       = file,
//                            onProgress = { sent, total ->
//                                progress = (sent.toFloat() / total.toFloat()).coerceIn(0f, 1f)
//                            },
//                            onStatus = { status = StatusState.Info(it) }
//                        )
//                        isSending = false
//                        if (ok) { status = StatusState.Success("Transfer complete!"); progress = 1f }
//                        else    { status = StatusState.Error("Transfer failed.") }
//                    }
//                }
//            )
//            Spacer(Modifier.height(28.dp))
//        }
//    }
//}
//
//@Preview
//@Composable
//fun PreviewOpenAirSenderScreen(){
//    OpenAirSenderScreen()
//}
//
//// ─── Status sealed class ──────────────────────────────────────────────────────
//sealed class StatusState {
//    object Idle : StatusState()
//    data class Info(val msg: String)    : StatusState()
//    data class Success(val msg: String) : StatusState()
//    data class Warning(val msg: String) : StatusState()
//    data class Error(val msg: String)   : StatusState()
//}
//
//// ─── Sub-components ───────────────────────────────────────────────────────────
//
//@Composable
//fun AppHeader() {
//    Row(verticalAlignment = Alignment.CenterVertically) {
//        Box(
//            modifier = Modifier
//                .size(40.dp)
//                .clip(RoundedCornerShape(12.dp))
//                .background(
//                    Brush.linearGradient(
//                        colors = listOf(Cyan500, Cyan400.copy(alpha = 0.6f)),
//                        start  = Offset.Zero,
//                        end    = Offset(40f, 40f)
//                    )
//                ),
//            contentAlignment = Alignment.Center
//        ) {
//            Icon(
//                imageVector        = Icons.Rounded.Send,
//                contentDescription = null,
//                tint               = Ink900,
//                modifier           = Modifier.size(20.dp)
//            )
//        }
//        Spacer(Modifier.width(14.dp))
//        Column {
//            Text(
//                text       = "OpenAir",
//                fontSize   = 22.sp,
//                fontWeight = FontWeight.Bold,
//                color      = Color.White,
//                letterSpacing = 0.3.sp
//            )
//            Text(
//                text       = "Local wireless file transfer",
//                fontSize   = 11.sp,
//                color      = Ink300,
//                letterSpacing = 0.5.sp
//            )
//        }
//    }
//}
//
//@Composable
//fun ScanButton(
//    isScanning: Boolean,
//    enabled: Boolean,
//    modifier: Modifier = Modifier,
//    onClick: () -> Unit
//) {
//    val infiniteTransition = rememberInfiniteTransition(label = "scan_pulse")
//    val alpha by infiniteTransition.animateFloat(
//        initialValue = 0.4f, targetValue = 1f,
//        animationSpec = infiniteRepeatable(tween(900), RepeatMode.Reverse),
//        label = "pulse"
//    )
//
//    Button(
//        onClick  = onClick,
//        enabled  = enabled,
//        modifier = modifier.height(50.dp),
//        shape    = RoundedCornerShape(14.dp),
//        colors   = ButtonDefaults.buttonColors(
//            containerColor = if (isScanning) Ink700 else Cyan500,
//            contentColor   = if (isScanning) Cyan400 else Ink900,
//            disabledContainerColor = Ink700,
//            disabledContentColor   = Ink400
//        ),
//        elevation = ButtonDefaults.buttonElevation(0.dp, 0.dp)
//    ) {
//        if (isScanning) {
//            Box(
//                modifier = Modifier
//                    .size(8.dp)
//                    .background(Cyan400.copy(alpha = alpha), CircleShape)
//            )
//            Spacer(Modifier.width(8.dp))
//            Text("Scanning…", fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
//        } else {
//            Icon(Icons.Rounded.Search, contentDescription = null, modifier = Modifier.size(16.dp))
//            Spacer(Modifier.width(8.dp))
//            Text("Scan", fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
//        }
//    }
//}
//
//@Composable
//fun PickFileButton(
//    hasPick: Boolean,
//    enabled: Boolean,
//    modifier: Modifier = Modifier,
//    onClick: () -> Unit
//) {
//    OutlinedButton(
//        onClick  = onClick,
//        enabled  = enabled,
//        modifier = modifier.height(50.dp),
//        shape    = RoundedCornerShape(14.dp),
//        colors   = ButtonDefaults.outlinedButtonColors(
//            contentColor         = if (hasPick) Cyan400 else Ink100,
//            disabledContentColor = Ink400
//        ),
//        border = androidx.compose.foundation.BorderStroke(
//            1.dp, if (hasPick) Cyan500.copy(alpha = 0.6f) else Ink600
//        )
//    ) {
//        Icon(
//            if (hasPick) Icons.Rounded.CheckCircle else Icons.Rounded.Add,
//            contentDescription = null,
//            modifier           = Modifier.size(16.dp)
//        )
//        Spacer(Modifier.width(8.dp))
//        Text(
//            if (hasPick) "Change File" else "Pick File",
//            fontSize   = 13.sp,
//            fontWeight = FontWeight.SemiBold
//        )
//    }
//}
//
//@Composable
//fun SectionLabel(
//    icon: androidx.compose.ui.graphics.vector.ImageVector,
//    label: String,
//    badge: String? = null
//) {
//    Row(verticalAlignment = Alignment.CenterVertically) {
//        Icon(icon, contentDescription = null, tint = Ink300, modifier = Modifier.size(14.dp))
//        Spacer(Modifier.width(6.dp))
//        Text(
//            text          = label.uppercase(),
//            fontSize      = 10.sp,
//            fontWeight    = FontWeight.Bold,
//            color         = Ink300,
//            letterSpacing = 1.2.sp
//        )
//        if (badge != null) {
//            Spacer(Modifier.width(8.dp))
//            Box(
//                modifier = Modifier
//                    .clip(CircleShape)
//                    .background(Cyan900)
//                    .padding(horizontal = 7.dp, vertical = 2.dp)
//            ) {
//                Text(badge, fontSize = 9.sp, color = Cyan400, fontWeight = FontWeight.Bold)
//            }
//        }
//    }
//}
//
//@Composable
//fun EmptyReceiversCard(isScanning: Boolean) {
//    Box(
//        modifier = Modifier
//            .fillMaxWidth()
//            .clip(RoundedCornerShape(16.dp))
//            .background(Ink800)
//            .border(1.dp, Ink700, RoundedCornerShape(16.dp))
//            .padding(24.dp),
//        contentAlignment = Alignment.Center
//    ) {
//        Column(horizontalAlignment = Alignment.CenterHorizontally) {
//            Icon(
//                Icons.Rounded.Face ?: Icons.Rounded.Info,
//                contentDescription = null,
//                tint               = Ink400,
//                modifier           = Modifier.size(32.dp)
//            )
//            Spacer(Modifier.height(10.dp))
//            Text(
//                if (isScanning) "Looking for receivers…" else "No receivers found",
//                color      = Ink300,
//                fontSize   = 13.sp,
//                fontWeight = FontWeight.Medium
//            )
//            if (!isScanning) {
//                Text(
//                    "Tap Scan to discover devices",
//                    color    = Ink400,
//                    fontSize = 11.sp,
//                    modifier = Modifier.padding(top = 2.dp)
//                )
//            }
//        }
//    }
//}
//
//@Composable
//fun ReceiverCard(
//    device: ReceiverDevice,
//    isSelected: Boolean,
//    enabled: Boolean,
//    onClick: () -> Unit
//) {
//    val animColor by animateColorAsState(
//        if (isSelected) Cyan900.copy(alpha = 0.5f) else Ink800,
//        animationSpec = tween(200),
//        label = "card_bg"
//    )
//    val borderColor by animateColorAsState(
//        if (isSelected) Cyan500.copy(alpha = 0.7f) else Ink700,
//        animationSpec = tween(200),
//        label = "card_border"
//    )
//
//    Box(
//        modifier = Modifier
//            .fillMaxWidth()
//            .clip(RoundedCornerShape(14.dp))
//            .background(animColor)
//            .border(1.dp, borderColor, RoundedCornerShape(14.dp))
//            .clickable(enabled = enabled, onClick = onClick)
//            .padding(horizontal = 16.dp, vertical = 14.dp)
//    ) {
//        Row(verticalAlignment = Alignment.CenterVertically) {
//            Box(
//                modifier = Modifier
//                    .size(36.dp)
//                    .clip(RoundedCornerShape(10.dp))
//                    .background(if (isSelected) Cyan900 else Ink700),
//                contentAlignment = Alignment.Center
//            ) {
//                Icon(
//                    Icons.Rounded.Home,
//                    contentDescription = null,
//                    tint     = if (isSelected) Cyan400 else Ink300,
//                    modifier = Modifier.size(18.dp)
//                )
//            }
//            Spacer(Modifier.width(14.dp))
//            Column(modifier = Modifier.weight(1f)) {
//                Text(
//                    device.name,
//                    color      = if (isSelected) Color.White else Ink100,
//                    fontWeight = FontWeight.SemiBold,
//                    fontSize   = 14.sp,
//                    maxLines   = 1,
//                    overflow   = TextOverflow.Ellipsis
//                )
//                Text(
//                    "${device.host.hostAddress}:${device.port}",
//                    color      = if (isSelected) Cyan400.copy(alpha = 0.8f) else Ink400,
//                    fontSize   = 11.sp,
//                    fontFamily = FontFamily.Monospace
//                )
//            }
//            if (isSelected) {
//                Icon(
//                    Icons.Rounded.CheckCircle,
//                    contentDescription = "Selected",
//                    tint     = Cyan400,
//                    modifier = Modifier.size(18.dp)
//                )
//            }
//        }
//    }
//}
//
//@Composable
//fun FileCard(picked: PickedFile?) {
//    Box(
//        modifier = Modifier
//            .fillMaxWidth()
//            .clip(RoundedCornerShape(16.dp))
//            .background(Ink800)
//            .border(1.dp, if (picked != null) Ink600 else Ink700, RoundedCornerShape(16.dp))
//            .padding(16.dp)
//    ) {
//        if (picked == null) {
//            Row(verticalAlignment = Alignment.CenterVertically) {
//                Icon(
//                    Icons.Rounded.Add,
//                    contentDescription = null,
//                    tint     = Ink600,
//                    modifier = Modifier.size(24.dp)
//                )
//                Spacer(Modifier.width(12.dp))
//                Text("No file selected", color = Ink400, fontSize = 13.sp)
//            }
//        } else {
//            Row(verticalAlignment = Alignment.CenterVertically) {
//                Box(
//                    modifier = Modifier
//                        .size(42.dp)
//                        .clip(RoundedCornerShape(10.dp))
//                        .background(Ink700),
//                    contentAlignment = Alignment.Center
//                ) {
//                    Text(
//                        text     = picked.name.substringAfterLast('.', "?").uppercase().take(3),
//                        color    = Cyan400,
//                        fontSize = 9.sp,
//                        fontWeight    = FontWeight.ExtraBold,
//                        fontFamily    = FontFamily.Monospace,
//                        letterSpacing = 0.5.sp
//                    )
//                }
//                Spacer(Modifier.width(14.dp))
//                Column(modifier = Modifier.weight(1f)) {
//                    Text(
//                        picked.name,
//                        color      = Color.White,
//                        fontWeight = FontWeight.SemiBold,
//                        fontSize   = 14.sp,
//                        maxLines   = 1,
//                        overflow   = TextOverflow.Ellipsis
//                    )
//                    Text(
//                        formatBytes(picked.size),
//                        color      = Ink300,
//                        fontSize   = 11.sp,
//                        fontFamily = FontFamily.Monospace
//                    )
//                }
//            }
//        }
//    }
//}
//
//@Composable
//fun TransferProgress(progress: Float, isSending: Boolean) {
//    val animProgress by animateFloatAsState(
//        targetValue    = progress,
//        animationSpec  = tween(300),
//        label          = "progress"
//    )
//
//    Column {
//        Row(
//            modifier = Modifier.fillMaxWidth(),
//            horizontalArrangement = Arrangement.SpaceBetween,
//            verticalAlignment     = Alignment.CenterVertically
//        ) {
//            Text(
//                "Transfer",
//                color      = Ink400,
//                fontSize   = 10.sp,
//                fontWeight = FontWeight.SemiBold,
//                letterSpacing = 0.8.sp
//            )
//            Text(
//                "${(animProgress * 100).toInt()}%",
//                color      = if (progress > 0f) Cyan400 else Ink400,
//                fontSize   = 10.sp,
//                fontWeight = FontWeight.Bold,
//                fontFamily = FontFamily.Monospace
//            )
//        }
//        Spacer(Modifier.height(6.dp))
//        Box(
//            modifier = Modifier
//                .fillMaxWidth()
//                .height(5.dp)
//                .clip(RoundedCornerShape(50))
//                .background(Ink700)
//        ) {
//            Box(
//                modifier = Modifier
//                    .fillMaxWidth(animProgress)
//                    .fillMaxHeight()
//                    .clip(RoundedCornerShape(50))
//                    .background(
//                        Brush.horizontalGradient(
//                            colors = listOf(Cyan500, Cyan400)
//                        )
//                    )
//            )
//        }
//    }
//}
//
//@Composable
//fun StatusRow(state: StatusState) {
//    val (icon, color, msg) = when (state) {
//        is StatusState.Idle    -> Triple(Icons.Rounded.Info,         Ink400,   "Ready")
//        is StatusState.Info    -> Triple(Icons.Rounded.Info,         Ink300,   state.msg)
//        is StatusState.Success -> Triple(Icons.Rounded.CheckCircle,  Green400, state.msg)
//        is StatusState.Warning -> Triple(Icons.Rounded.Warning,      Amber400, state.msg)
//        is StatusState.Error   -> Triple(Icons.Rounded.Close,        Red400,   state.msg)
//    }
//
//    Row(
//        verticalAlignment = Alignment.CenterVertically,
//        modifier          = Modifier
//            .fillMaxWidth()
//            .clip(RoundedCornerShape(10.dp))
//            .background(Ink800)
//            .padding(horizontal = 14.dp, vertical = 10.dp)
//    ) {
//        Icon(icon, contentDescription = null, tint = color, modifier = Modifier.size(14.dp))
//        Spacer(Modifier.width(10.dp))
//        Text(
//            text     = msg,
//            color    = color.copy(alpha = 0.9f),
//            fontSize = 12.sp,
//            maxLines = 2,
//            overflow = TextOverflow.Ellipsis
//        )
//    }
//}
//
//@Composable
//fun SendButton(isSending: Boolean, enabled: Boolean, onClick: () -> Unit) {
//    Button(
//        onClick  = onClick,
//        enabled  = enabled,
//        modifier = Modifier
//            .fillMaxWidth()
//            .height(56.dp),
//        shape    = RoundedCornerShape(16.dp),
//        colors   = ButtonDefaults.buttonColors(
//            containerColor         = Cyan500,
//            contentColor           = Ink900,
//            disabledContainerColor = Ink700,
//            disabledContentColor   = Ink400
//        ),
//        elevation = ButtonDefaults.buttonElevation(0.dp, 0.dp)
//    ) {
//        if (isSending) {
//            CircularProgressIndicator(
//                color    = Cyan400,
//                modifier = Modifier.size(18.dp),
//                strokeWidth = 2.dp
//            )
//            Spacer(Modifier.width(10.dp))
//            Text("Sending…", fontSize = 15.sp, fontWeight = FontWeight.Bold, letterSpacing = 0.3.sp)
//        } else {
//            Icon(Icons.Rounded.Send, contentDescription = null, modifier = Modifier.size(18.dp))
//            Spacer(Modifier.width(10.dp))
//            Text("Send File", fontSize = 15.sp, fontWeight = FontWeight.Bold, letterSpacing = 0.3.sp)
//        }
//    }
//}
//
//// ─── Network & Utility (unchanged logic) ─────────────────────────────────────
//
//fun discoverReceivers(
//    context: Context,
//    onFound: (ReceiverDevice) -> Unit,
//    onStatus: (String) -> Unit,
//    onDone: () -> Unit
//) {
//    val nsd = context.getSystemService(Context.NSD_SERVICE) as NsdManager
//
//    val discoveryListener = object : NsdManager.DiscoveryListener {
//        override fun onDiscoveryStarted(serviceType: String) { onStatus("Discovery started") }
//
//        override fun onServiceFound(serviceInfo: NsdServiceInfo) {
//            if (!serviceInfo.serviceType.contains("_openair._tcp")) return
//            nsd.resolveService(serviceInfo, object : NsdManager.ResolveListener {
//                override fun onResolveFailed(si: NsdServiceInfo, errorCode: Int) {}
//                override fun onServiceResolved(resolved: NsdServiceInfo) {
//                    val host      = resolved.host ?: return
//                    val port      = resolved.port.takeIf { it > 0 } ?: DEFAULT_PORT
//                    val finalHost = pickIPv4(host) ?: host
//                    onFound(ReceiverDevice(name = resolved.serviceName, host = finalHost, port = port))
//                }
//            })
//        }
//
//        override fun onServiceLost(serviceInfo: NsdServiceInfo) {}
//        override fun onDiscoveryStopped(serviceType: String) { onStatus("Discovery stopped"); onDone() }
//        override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {
//            onStatus("Discovery failed (code=$errorCode)")
//            nsd.stopServiceDiscovery(this); onDone()
//        }
//        override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) {
//            nsd.stopServiceDiscovery(this); onDone()
//        }
//    }
//
//    nsd.discoverServices(SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, discoveryListener)
//    Thread { Thread.sleep(5000); try { nsd.stopServiceDiscovery(discoveryListener) } catch (_: Exception) {} }.start()
//}
//
//fun pickIPv4(host: InetAddress): InetAddress? = if (host is Inet4Address) host else null
//
//suspend fun sendFileToReceiver(
//    context: Context,
//    receiver: ReceiverDevice,
//    file: PickedFile,
//    onProgress: (sent: Long, total: Long) -> Unit,
//    onStatus: (String) -> Unit
//): Boolean = withContext(Dispatchers.IO) {
//    try {
//        val sha = computeSha256(context, file.uri)
//        onStatus("Connecting to ${receiver.host.hostAddress}:${receiver.port}…")
//
//        Socket(receiver.host, receiver.port).use { socket ->
//            socket.tcpNoDelay = true
//            val out = BufferedOutputStream(socket.getOutputStream())
//            val `in` = BufferedReader(InputStreamReader(socket.getInputStream()))
//
//            val header = """{"name":"${escapeJson(file.name)}","size":${file.size},"sha256":"$sha"}"""
//            out.write((header + "\n").toByteArray(Charsets.UTF_8))
//            out.flush()
//
//            val response = `in`.readLine()?.trim() ?: ""
//            if (!response.equals("ACCEPT", ignoreCase = true)) {
//                onStatus("Receiver rejected: $response"); return@withContext false
//            }
//            onStatus("Sending…")
//
//            val buffer = ByteArray(64 * 1024)
//            var sent = 0L
//            context.contentResolver.openInputStream(file.uri).use { raw ->
//                if (raw == null) throw IllegalStateException("Cannot open file stream")
//                val input = BufferedInputStream(raw)
//                while (true) {
//                    val read = input.read(buffer)
//                    if (read == -1) break
//                    out.write(buffer, 0, read)
//                    sent += read.toLong()
//                    onProgress(sent, file.size)
//                }
//            }
//            out.flush()
//            if (sent != file.size) { onStatus("Warning: sent $sent, expected ${file.size}"); return@withContext false }
//            return@withContext true
//        }
//    } catch (e: Exception) {
//        onStatus("Error: ${e.message}"); return@withContext false
//    }
//}
//
//fun getFileMeta(context: Context, uri: Uri): Pair<String, Long>? {
//    val cursor = context.contentResolver.query(uri, null, null, null, null) ?: return null
//    cursor.use {
//        val nameIndex = it.getColumnIndex(OpenableColumns.DISPLAY_NAME)
//        val sizeIndex = it.getColumnIndex(OpenableColumns.SIZE)
//        if (!it.moveToFirst()) return null
//        val name = if (nameIndex != -1) it.getString(nameIndex) else "file"
//        val size = if (sizeIndex != -1) it.getLong(sizeIndex) else -1L
//        if (size <= 0) return null
//        return Pair(name, size)
//    }
//}
//
//suspend fun computeSha256(context: Context, uri: Uri): String = withContext(Dispatchers.IO) {
//    val digest = MessageDigest.getInstance("SHA-256")
//    val buffer = ByteArray(64 * 1024)
//    context.contentResolver.openInputStream(uri).use { raw ->
//        if (raw == null) throw IllegalStateException("Cannot open file stream")
//        val input = BufferedInputStream(raw)
//        while (true) { val read = input.read(buffer); if (read == -1) break; digest.update(buffer, 0, read) }
//    }
//    digest.digest().joinToString("") { "%02x".format(it) }
//}
//
//fun escapeJson(s: String) = s.replace("\\", "\\\\").replace("\"", "\\\"")
//
//fun formatBytes(n: Long): String {
//    val kb = 1024.0; val mb = kb * 1024; val gb = mb * 1024
//    return when {
//        n >= gb -> "%.2f GB".format(n / gb)
//        n >= mb -> "%.2f MB".format(n / mb)
//        n >= kb -> "%.2f KB".format(n / kb)
//        else    -> "$n B"
//    }
//}