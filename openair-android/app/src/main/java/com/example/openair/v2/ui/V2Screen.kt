package com.example.openair.v2.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import com.example.openair.v2.DeviceRow
import com.example.openair.v2.V2UiState
import com.example.openair.v2.V2ViewModel

/**
 * V2Screen is the shell over the gomobile core: pair, discover, send, receive.
 *
 * It is deliberately plain. The v1 UI in ../ui is the styled one; this exists to
 * drive the v2 stack end to end on a real device, and every element here maps to
 * one operation on the binding rather than to a design.
 */
@Composable
fun V2Screen(vm: V2ViewModel = viewModel()) {
    val state by vm.state.collectAsStateWithLifecycle()

    val pickFile = rememberLauncherForActivityResult(
        ActivityResultContracts.OpenDocument(),
    ) { uri -> uri?.let(vm::addFile) }

    Scaffold { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            ThisDevice(state)
            ReceiveControls(state, vm)
            Files(state, onPick = { pickFile.launch(arrayOf("*/*")) }, onClear = vm::clearFiles)
            ClipboardControls(state, vm)
            PairingControls(state, vm)
            Devices(state, vm)
            if (state.status.isNotEmpty()) {
                Text(state.status, style = MaterialTheme.typography.bodySmall)
            }
        }
    }

    state.sasPrompt?.let { prompt ->
        AlertDialog(
            onDismissRequest = { vm.answerSas(false) },
            title = { Text("Do both screens show these digits?") },
            text = {
                Column {
                    Text(
                        prompt.digits,
                        fontFamily = FontFamily.Monospace,
                        fontSize = 34.sp,
                        fontWeight = FontWeight.Bold,
                    )
                    Spacer(Modifier.height(8.dp))
                    Text("${prompt.peerName.ifEmpty { "unnamed device" }} · ${prompt.peerFingerprint}")
                    Spacer(Modifier.height(8.dp))
                    // The comparison is the whole security of pairing (§5.2).
                    // Saying so is the point of the dialog, not decoration.
                    Text(
                        "If the two devices show different digits, something is " +
                            "intercepting this pairing. Answer no.",
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
            },
            confirmButton = { TextButton(onClick = { vm.answerSas(true) }) { Text("They match") } },
            dismissButton = { TextButton(onClick = { vm.answerSas(false) }) { Text("They differ") } },
        )
    }

    state.offerPrompt?.let { prompt ->
        AlertDialog(
            onDismissRequest = { vm.answerOffer(false) },
            title = { Text("Accept ${prompt.fileCount} file(s)?") },
            text = {
                Column {
                    Text("${prompt.peerName.ifEmpty { "unnamed device" }} · ${prompt.peerFingerprint}")
                    Spacer(Modifier.height(4.dp))
                    Text("${prompt.firstPath} · ${humanBytes(prompt.totalBytes)}")
                }
            },
            confirmButton = { TextButton(onClick = { vm.answerOffer(true) }) { Text("Accept") } },
            dismissButton = { TextButton(onClick = { vm.answerOffer(false) }) { Text("Decline") } },
        )
    }

    state.offerToShow?.let { offer ->
        AlertDialog(
            onDismissRequest = vm::cancelPairingOffer,
            title = { Text("Pair with this device") },
            text = {
                Column {
                    Text("Type this on the other device, or scan it:")
                    Spacer(Modifier.height(8.dp))
                    Text(
                        state.offerGrouped.ifEmpty { offer },
                        fontFamily = FontFamily.Monospace,
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
            },
            confirmButton = { TextButton(onClick = vm::cancelPairingOffer) { Text("Cancel") } },
        )
    }
}

@Composable
private fun ThisDevice(state: V2UiState) {
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(12.dp)) {
            Text("This device", style = MaterialTheme.typography.labelMedium)
            Text(state.fingerprint, fontFamily = FontFamily.Monospace)
            Text(
                "${state.pairedCount} paired · files land in ${state.destination}",
                style = MaterialTheme.typography.bodySmall,
            )
        }
    }
}

@Composable
private fun ReceiveControls(state: V2UiState, vm: V2ViewModel) {
    Row(
        Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (state.receiving) {
            OutlinedButton(onClick = vm::stopReceiving) { Text("Stop receiving") }
            Text(state.listenAddr, style = MaterialTheme.typography.bodySmall)
        } else {
            Button(onClick = vm::startReceiving) { Text("Start receiving") }
            OutlinedButton(onClick = vm::startDiscovery) { Text("Find devices") }
        }
    }
    if (state.incoming.active) {
        Column(Modifier.fillMaxWidth()) {
            Text("Receiving ${humanBytes(state.incoming.done)} / ${humanBytes(state.incoming.total)}")
            LinearProgressIndicator(
                progress = { state.incoming.fraction },
                modifier = Modifier.fillMaxWidth(),
            )
        }
    }
}

@Composable
private fun Files(state: V2UiState, onPick: () -> Unit, onClear: () -> Unit) {
    Row(
        Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Button(onClick = onPick) { Text("Add file") }
        if (state.picked.isNotEmpty()) {
            OutlinedButton(onClick = onClear) { Text("Clear (${state.picked.size})") }
        }
    }
    state.picked.forEach { f ->
        Text("• ${f.name} · ${humanBytes(f.bytes)}", style = MaterialTheme.typography.bodySmall)
    }
    if (state.sending.active) {
        Column(Modifier.fillMaxWidth()) {
            Text("Sending ${humanBytes(state.sending.done)} / ${humanBytes(state.sending.total)}")
            LinearProgressIndicator(
                progress = { state.sending.fraction },
                modifier = Modifier.fillMaxWidth(),
            )
        }
    }
}

/**
 * The clipboard half of M5. Text typed here is what gets pushed; left empty,
 * the push takes whatever is on this phone's clipboard -- a read the system
 * only permits while the app is in front, which a button press guarantees.
 */
@Composable
private fun ClipboardControls(state: V2UiState, vm: V2ViewModel) {
    OutlinedTextField(
        value = state.clipboardText,
        onValueChange = vm::setClipboardText,
        label = { Text("clipboard text (empty = this phone's clipboard)") },
        singleLine = true,
        modifier = Modifier.fillMaxWidth(),
    )
    if (state.lastClipboard.isNotEmpty()) {
        Text(
            "from ${state.lastClipboardFrom}: ${state.lastClipboard.take(120)}",
            style = MaterialTheme.typography.bodySmall,
        )
    }
}

@Composable
private fun PairingControls(state: V2UiState, vm: V2ViewModel) {
    var code by remember { mutableStateOf("") }

    Row(
        Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Button(onClick = vm::showPairingOffer) { Text("Show pairing code") }
    }
    Row(
        Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        OutlinedTextField(
            value = code,
            onValueChange = { code = it },
            label = { Text("code from the other device") },
            singleLine = true,
            modifier = Modifier.weight(1f),
        )
        Button(
            onClick = {
                vm.pairWithCode(code.trim())
                code = ""
            },
            enabled = code.isNotBlank(),
        ) { Text("Pair") }
    }
}

@Composable
private fun Devices(state: V2UiState, vm: V2ViewModel) {
    Text("Devices on this network", style = MaterialTheme.typography.labelMedium)
    if (state.devices.isEmpty()) {
        Text(
            "None yet. The other device needs OpenAir running on the same network.",
            style = MaterialTheme.typography.bodySmall,
        )
        return
    }
    LazyColumn(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        items(state.devices, key = { it.deviceId }) { d -> DeviceCard(d, vm) }
    }
}

@Composable
private fun DeviceCard(d: DeviceRow, vm: V2ViewModel) {
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(10.dp)) {
            Text(d.displayName.ifEmpty { "unnamed device" })
            Text(
                "${d.fingerprint} · ${d.addr} · via ${d.via}",
                fontFamily = FontFamily.Monospace,
                style = MaterialTheme.typography.bodySmall,
            )
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (d.paired) {
                    Button(onClick = { vm.sendTo(d) }) { Text("Send") }
                    OutlinedButton(onClick = { vm.pushClipboard(d) }) { Text("Clipboard") }
                    OutlinedButton(onClick = { vm.unpair(d.deviceId) }) { Text("Unpair") }
                } else {
                    // Discovery found it, but nothing has authenticated it. A
                    // transfer would be refused at both ends until it is paired.
                    Text("not paired", style = MaterialTheme.typography.bodySmall)
                }
            }
        }
    }
}

private fun humanBytes(n: Long): String {
    if (n < 1024) return "$n B"
    var div = 1024L
    var exp = 0
    var m = n / 1024
    while (m >= 1024) {
        div *= 1024
        m /= 1024
        exp++
    }
    return String.format("%.1f %ciB", n.toDouble() / div, "KMGTPE"[exp])
}
