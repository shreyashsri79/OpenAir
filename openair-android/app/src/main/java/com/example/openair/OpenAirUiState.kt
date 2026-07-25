package com.example.openair

import androidx.compose.runtime.Immutable
import java.util.UUID

// ── Domain models ─────────────────────────────────────────────────────────────

@Immutable
data class DeviceInfo(
    val id: String,
    val name: String,
    /** Signal strength 0–100 */
    val rssi: Int,
    val isConnected: Boolean = false
)

@Immutable
data class FileAttachment(
    val id: String,
    val name: String,
    val sizeLabel: String,
    val mimeType: String
)

@Immutable
data class TransferState(
    val fileName: String,
    /** 0f..1f — ratio calculated by the ViewModel, never raw bytes */
    val progress: Float,
    val direction: TransferDirection,
    /** Human-readable status line emitted by the engine (e.g. "RTT 4 ms · 120 MB/s") */
    val transferStatus: String = "",
)

enum class TransferDirection { SENDING, RECEIVING }

/**
 * Snapshot of a successfully completed incoming transfer.
 * [mediaUri] is a string so [OpenAirUiState] stays free of Android framework types.
 */
@Immutable
data class ReceivedFile(
    val id         : String,
    val name       : String,
    val sizeLabel  : String,
    val mediaUri   : String,   // android.net.Uri.toString()
    val mimeType   : String = "application/octet-stream",
    /** Epoch millis when the transfer completed. */
    val receivedAt : Long   = 0L,
)

/**
 * A pending accept/reject prompt shown to the user when a sender connects.
 * [id] is a UUID used to correlate the user's decision back to the receiver.
 */
@Immutable
data class IncomingRequest(
    val id        : String = UUID.randomUUID().toString(),
    val fileName  : String,
    val sizeLabel : String,
)

// ── Root UI State ─────────────────────────────────────────────────────────────

/**
 * Single source-of-truth for every visual decision in OpenAirContent.
 * No Android framework types; trivially testable.
 */
@Immutable
data class OpenAirUiState(
    val deviceName       : String                  = "My Phone",
    val isReceiverOn     : Boolean                 = false,
    val isScanning       : Boolean                 = false,
    val connectedDevices : List<DeviceInfo>        = emptyList(),
    val availableDevices : List<DeviceInfo>        = emptyList(),
    val attachedFiles    : List<FileAttachment>    = emptyList(),
    val pastedText       : String                  = "",
    val activeTransfer   : TransferState?          = null,
    val error            : String?                 = null,
    // ── Receiver state ─────────────────────────────────────────────────────
    /** Port the receiver is listening on; shown in the UI for desktop senders. */
    val receiverPort     : Int                     = 9000,
    /** Pending accept/reject prompt — non-null while a sender is waiting. */
    val incomingRequest  : IncomingRequest?        = null,
    /** History of completed incoming transfers (most recent last). */
    val receivedFiles    : List<ReceivedFile>      = emptyList(),
)

// ── Event contract (all lambdas hoisted to parent) ────────────────────────────

/**
 * Exhaustive set of user-initiated events. Passed as a single object so
 * OpenAirContent never needs to know where events go.
 */
data class OpenAirCallbacks(
    val onToggleReceiver    : () -> Unit                   = {},
    val onScanClick         : () -> Unit                   = {},
    val onDeviceConnect     : (DeviceInfo) -> Unit         = {},
    val onDeviceDisconnect  : (DeviceInfo) -> Unit         = {},
    val onAttachFile        : () -> Unit                   = {},
    val onAttachFolder      : () -> Unit                   = {},
    val onRemoveFile        : (FileAttachment) -> Unit     = {},
    val onPasteTextChange   : (String) -> Unit             = {},
    val onSendClick         : () -> Unit                   = {},
    val onDismissError      : () -> Unit                   = {},
    // ── Receiver callbacks ────────────────────────────────────────────────
    /** User tapped Accept on the incoming-transfer dialog. */
    val onAcceptTransfer    : () -> Unit                   = {},
    /** User tapped Reject on the incoming-transfer dialog. */
    val onRejectTransfer    : () -> Unit                   = {},
    /** User tapped a row in the Received Files section — open the file. */
    val onOpenReceivedFile  : (ReceivedFile) -> Unit       = {},
    /** User tapped Clear on the Received Files section header. */
    val onClearReceived     : () -> Unit                   = {},
)

// ── Preview / mock data ───────────────────────────────────────────────────────

val MockDevices = listOf(
    DeviceInfo("d1", "Galaxy S25 Ultra", rssi = 88, isConnected = true),
    DeviceInfo("d2", "OnePlus 13",       rssi = 62, isConnected = false),
    DeviceInfo("d3", "iPhone 16 Pro",    rssi = 45, isConnected = false)
)

val MockFiles = listOf(
    FileAttachment("f1", "design_v3.pdf", "2.4 MB", "application/pdf"),
    FileAttachment("f2", "sunset.jpg",    "1.1 MB", "image/jpeg"),
    FileAttachment("f3", "notes.txt",     "4 KB",   "text/plain")
)

val MockUiState = OpenAirUiState(
    deviceName       = "Pixel 9 Pro",
    isReceiverOn     = true,
    isScanning       = true,
    connectedDevices = MockDevices.filter { it.isConnected },
    availableDevices = MockDevices.filter { !it.isConnected },
    attachedFiles    = MockFiles,
    pastedText       = "",
    activeTransfer   = TransferState(
        fileName       = "sunset.jpg",
        progress       = 0.62f,
        direction      = TransferDirection.SENDING,
        transferStatus = "RTT 4 ms · 120 MB/s · 3 workers"
    )
)
