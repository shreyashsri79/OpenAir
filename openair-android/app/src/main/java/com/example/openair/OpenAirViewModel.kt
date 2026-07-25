package com.example.openair

import android.app.Application
import android.net.Uri
import android.provider.OpenableColumns
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.example.openair.core.Device
import com.example.openair.core.NsdDiscoveryManager
import com.example.openair.core.OpenAirReceiver
import com.example.openair.core.OpenAirSender
import com.example.openair.core.SendItem
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.receiveAsFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.util.UUID

/**
 * Thin ViewModel — bridges UI events to [OpenAirSender] and [OpenAirReceiver].
 *
 * ### Architecture contract
 * - The UI layer knows **nothing** about TCP, sockets, or chunks.
 * - All networking types are confined to this class and the core package.
 * - [OpenAirUiState] is the only surface exposed upward.
 *
 * ### Receiver lifecycle
 * [toggleReceiver] starts/stops [OpenAirReceiver].  When a sender connects:
 *   1. [OpenAirReceiver.onIncoming] fires → ViewModel posts [IncomingRequest] to UiState.
 *   2. UI renders an accept/reject dialog.
 *   3. User taps → [acceptTransfer] / [rejectTransfer] resolves a [CompletableDeferred].
 *   4. Receiver gets the answer, sends ACCEPT/REJECT, and begins streaming.
 *
 * ### Transfer lifecycle
 * [sendFiles] launches inside [viewModelScope].  If the user navigates away,
 * the ViewModel is cleared, the scope is cancelled, and cooperative cancellation
 * unwinds the NIO [java.nio.channels.SocketChannel]s.
 */
class OpenAirViewModel(app: Application) : AndroidViewModel(app) {

    private val _uiState = MutableStateFlow(OpenAirUiState())
    val uiState: StateFlow<OpenAirUiState> = _uiState.asStateFlow()

    /** Handle to the active outbound transfer job; kept for explicit cancellation. */
    private var transferJob: Job? = null

    /**
     * Pending deferred for an incoming transfer.
     * Created by [OpenAirReceiver.onIncoming] → stored here → resolved by
     * [acceptTransfer] / [rejectTransfer].
     */
    private var incomingDeferred: CompletableDeferred<Boolean>? = null

    // ── Core engines ──────────────────────────────────────────────────────────

    private val discoveryManager = NsdDiscoveryManager(app.applicationContext)

    init {
        // Discovery runs continuously from app open — no manual scan needed.
        startScan()
        // Restore the all-time received-files history from disk.
        viewModelScope.launch(Dispatchers.IO) {
            val history = loadReceivedHistory()
            if (history.isNotEmpty()) {
                withContext(Dispatchers.Main.immediate) {
                    _uiState.update { it.copy(receivedFiles = history) }
                }
            }
        }
    }

    // ── Receiver toggle ───────────────────────────────────────────────────────

    fun toggleReceiver() {
        val turningOn = !_uiState.value.isReceiverOn

        if (turningOn) {
            startReceiver()
        } else {
            stopReceiver()
        }
    }

    private fun startReceiver() {
        val ctx = getApplication<Application>().applicationContext

        OpenAirReceiver.onIncoming = { fileName, sizeLabel ->
            // Create a deferred that the user's decision will resolve.
            val deferred = CompletableDeferred<Boolean>()
            incomingDeferred = deferred

            val request = IncomingRequest(
                id        = UUID.randomUUID().toString(),
                fileName  = fileName,
                sizeLabel = sizeLabel,
            )

            // Post incoming request to UI (main thread safe — StateFlow.update is atomic).
            _uiState.update { it.copy(incomingRequest = request) }

            deferred  // return to receiver so it can await()
        }

        OpenAirReceiver.onProgress = { bytesReceived, total ->
            val ratio = if (total > 0L) bytesReceived.toFloat() / total.toFloat() else 0f
            _uiState.update { state ->
                state.copy(
                    activeTransfer = state.activeTransfer?.copy(
                        progress       = ratio.coerceIn(0f, 1f),
                        transferStatus = "${formatBytes(bytesReceived)} / ${formatBytes(total)}"
                    )
                )
            }
        }

        OpenAirReceiver.onComplete = { fileName, uri ->
            val sizeLabel = _uiState.value.activeTransfer?.transferStatus
                ?.substringAfterLast("/ ") ?: ""
            val received = ReceivedFile(
                id         = UUID.randomUUID().toString(),
                name       = fileName,
                sizeLabel  = sizeLabel,
                mediaUri   = uri.toString(),
                mimeType   = guessMimeType(fileName),
                receivedAt = System.currentTimeMillis(),
            )
            // Mark IS_PENDING = 0 in MediaStore so the file appears in Downloads.
            OpenAirReceiver.markMediaStoreComplete(ctx, uri)

            _uiState.update { state ->
                state.copy(
                    activeTransfer = null,
                    receivedFiles  = state.receivedFiles + received,
                )
            }
            persistReceivedHistory(_uiState.value.receivedFiles)
        }

        OpenAirReceiver.onError = { message ->
            _uiState.update { it.copy(error = message) }
        }

        OpenAirReceiver.start(ctx)

        _uiState.update { it.copy(isReceiverOn = true, receiverPort = OpenAirReceiver.PORT) }
    }

    private fun stopReceiver() {
        val ctx = getApplication<Application>().applicationContext
        OpenAirReceiver.stop(ctx)
        // Clear any pending accept/reject dialog.
        incomingDeferred?.cancel()
        incomingDeferred = null
        _uiState.update { it.copy(isReceiverOn = false, incomingRequest = null) }
    }

    // ── Incoming transfer — accept / reject ───────────────────────────────────

    /**
     * Called by the UI when the user taps **Accept** on the incoming-transfer
     * dialog.  Resolves the pending deferred so [OpenAirReceiver] sends ACCEPT,
     * and updates UiState with a RECEIVING [TransferState].
     */
    fun acceptTransfer() {
        val request = _uiState.value.incomingRequest ?: return
        _uiState.update { state ->
            state.copy(
                incomingRequest = null,
                activeTransfer  = TransferState(
                    fileName       = request.fileName,
                    progress       = 0f,
                    direction      = TransferDirection.RECEIVING,
                    transferStatus = "Receiving…",
                )
            )
        }
        incomingDeferred?.complete(true)
        incomingDeferred = null
    }

    /**
     * Called by the UI when the user taps **Reject**.
     * Resolves the deferred with `false` → receiver sends REJECT.
     */
    fun rejectTransfer() {
        incomingDeferred?.complete(false)
        incomingDeferred = null
        _uiState.update { it.copy(incomingRequest = null) }
    }

    // ── Discovery ─────────────────────────────────────────────────────────────

    fun startScan() {
        _uiState.update { it.copy(isScanning = true, availableDevices = emptyList()) }

        discoveryManager.startScan(
            onStatus = { status ->
                if (status.startsWith("Discovery failed") || status.startsWith("Scan stopped")) {
                    viewModelScope.launch(Dispatchers.Main.immediate) {
                        _uiState.update { it.copy(isScanning = false) }
                    }
                }
            },
            onFound = { device ->
                viewModelScope.launch(Dispatchers.Main.immediate) {
                    val newDevice = DeviceInfo(
                        id          = "${device.host}:${device.port}",
                        name        = device.name,
                        rssi        = 100,
                        isConnected = false,
                    )
                    _uiState.update { state ->
                        // Already connected → don't duplicate into available list.
                        if (state.connectedDevices.any { it.id == newDevice.id }) return@update state
                        val updated = state.availableDevices
                            .filterNot { it.id == newDevice.id } + newDevice
                        state.copy(availableDevices = updated)
                    }
                }
            },
            onLost = { serviceName ->
                viewModelScope.launch(Dispatchers.Main.immediate) {
                    _uiState.update { state ->
                        state.copy(
                            availableDevices = state.availableDevices.filterNot { it.name == serviceName }
                        )
                    }
                }
            }
        )
    }

    fun stopScan() {
        discoveryManager.stopScan()
        _uiState.update { it.copy(isScanning = false) }
    }

    // ── Connections ───────────────────────────────────────────────────────────

    fun connectDevice(device: DeviceInfo) {
        _uiState.update { state ->
            if (state.connectedDevices.any { it.id == device.id }) return@update state
            state.copy(
                availableDevices = state.availableDevices.filterNot { it.id == device.id },
                connectedDevices = state.connectedDevices + device.copy(isConnected = true),
            )
        }
    }

    fun disconnectDevice(device: DeviceInfo) {
        _uiState.update { state ->
            state.copy(
                connectedDevices = state.connectedDevices.filterNot { it.id == device.id },
                availableDevices = state.availableDevices.filterNot { it.id == device.id } +
                    device.copy(isConnected = false),
            )
        }
    }

    // ── Files & text ──────────────────────────────────────────────────────────

    fun onFilePicked(uri: Uri) {
        viewModelScope.launch(Dispatchers.IO) {
            val cr       = getApplication<Application>().contentResolver
            val mimeType = cr.getType(uri) ?: "application/octet-stream"
            var displayName = uri.lastPathSegment ?: "file"
            var sizeBytes   = 0L

            cr.query(uri, null, null, null, null)?.use { cursor ->
                val nameCol = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                val sizeCol = cursor.getColumnIndex(OpenableColumns.SIZE)
                if (cursor.moveToFirst()) {
                    if (nameCol >= 0) displayName = cursor.getString(nameCol)
                    if (sizeCol >= 0) sizeBytes   = cursor.getLong(sizeCol)
                }
            }

            val id         = UUID.randomUUID().toString()
            val attachment = FileAttachment(
                id        = id,
                name      = displayName,
                sizeLabel = formatBytes(sizeBytes),
                mimeType  = mimeType,
            )
            fileUriMap[id] = uri

            withContext(Dispatchers.Main.immediate) {
                _uiState.update { state ->
                    state.copy(attachedFiles = state.attachedFiles + attachment)
                }
            }
        }
    }

    fun attachFile() { /* file picker launcher lives in OpenAirScreen */ }

    /**
     * Zips a SAF folder tree (picked via OpenDocumentTree) into the app's cache
     * dir, then attaches the resulting .zip like any other file.
     */
    fun onFolderPicked(treeUri: Uri) {
        viewModelScope.launch(Dispatchers.IO) {
            val ctx  = getApplication<Application>().applicationContext
            val root = androidx.documentfile.provider.DocumentFile.fromTreeUri(ctx, treeUri)
            if (root == null || !root.isDirectory) {
                withContext(Dispatchers.Main.immediate) {
                    _uiState.update { it.copy(error = "Cannot read that folder.") }
                }
                return@launch
            }

            val folderName = root.name ?: "folder"
            val zipFile = java.io.File(
                ctx.cacheDir,
                "oa_zip_${System.currentTimeMillis()}_$folderName.zip"
            )

            try {
                java.util.zip.ZipOutputStream(
                    java.io.BufferedOutputStream(java.io.FileOutputStream(zipFile))
                ).use { zos ->
                    fun walk(dir: androidx.documentfile.provider.DocumentFile, path: String) {
                        for (child in dir.listFiles()) {
                            val name = child.name ?: continue
                            val childPath = "$path/$name"
                            if (child.isDirectory) {
                                walk(child, childPath)
                            } else {
                                zos.putNextEntry(java.util.zip.ZipEntry(childPath))
                                ctx.contentResolver.openInputStream(child.uri)?.use { it.copyTo(zos) }
                                zos.closeEntry()
                            }
                        }
                    }
                    walk(root, folderName)
                }
            } catch (e: Exception) {
                zipFile.delete()
                withContext(Dispatchers.Main.immediate) {
                    _uiState.update { it.copy(error = "Zip failed: ${e.message}") }
                }
                return@launch
            }

            val id = UUID.randomUUID().toString()
            val attachment = FileAttachment(
                id        = id,
                name      = "$folderName.zip",
                sizeLabel = formatBytes(zipFile.length()),
                mimeType  = "application/zip",
            )
            fileUriMap[id] = Uri.fromFile(zipFile)

            withContext(Dispatchers.Main.immediate) {
                _uiState.update { state ->
                    state.copy(attachedFiles = state.attachedFiles + attachment)
                }
            }
        }
    }

    fun removeFile(file: FileAttachment) {
        fileUriMap.remove(file.id)?.let { uri ->
            // Zipped folders live in our cache dir — reclaim the space.
            if (uri.scheme == "file") {
                uri.path?.let { p ->
                    val f = java.io.File(p)
                    if (f.parentFile == getApplication<Application>().cacheDir) f.delete()
                }
            }
        }
        _uiState.update { state ->
            state.copy(attachedFiles = state.attachedFiles.filterNot { it.id == file.id })
        }
    }

    fun updatePastedText(text: String) {
        _uiState.update { it.copy(pastedText = text) }
    }

    // ── Outbound transfer ─────────────────────────────────────────────────────

    /**
     * Alias wired to the Send button in [OpenAirCallbacks].
     */
    fun send() {
        val targets = _uiState.value.connectedDevices
        val files   = _uiState.value.attachedFiles
        val text    = _uiState.value.pastedText.trim()

        if (targets.isEmpty()) {
            _uiState.update { it.copy(error = "Connect at least one device first.") }
            return
        }
        if (files.isEmpty() && text.isEmpty()) {
            _uiState.update { it.copy(error = "Attach a file/folder or paste some text first.") }
            return
        }

        val coreDevices = targets.mapNotNull { info ->
            val sep  = info.id.lastIndexOf(':')
            if (sep == -1) return@mapNotNull null
            val host = info.id.substring(0, sep)
            val port = info.id.substring(sep + 1).toIntOrNull() ?: return@mapNotNull null
            Device(name = info.name, host = host, port = port)
        }

        if (coreDevices.isEmpty()) {
            _uiState.update { it.copy(error = "No resolvable devices — try scanning again.") }
            return
        }

        val sendItems = buildList {
            files.mapNotNullTo(this) { attachment ->
                fileUriMap[attachment.id]?.let { uri ->
                    SendItem.UriFile(uri = uri, name = attachment.name)
                }
            }
            if (text.isNotEmpty()) {
                val stamp = java.text.SimpleDateFormat("yyyyMMdd-HHmmss", java.util.Locale.US)
                    .format(java.util.Date())
                add(SendItem.TextFile(name = "openair-text-$stamp.txt", bytes = text.toByteArray()))
            }
        }

        if (sendItems.isEmpty()) {
            _uiState.update { it.copy(error = "Nothing valid to send — try attaching again.") }
            return
        }

        sendFiles(items = sendItems, targets = coreDevices)
    }

    /**
     * Bridges [OpenAirSender.sendToMany] to [OpenAirUiState].
     *
     * ### Progress throttling
     * A [Channel.CONFLATED] channel acts as a "latest-only" mailbox: the engine
     * thread calls `trySend` (non-blocking); the consumer collects at its own
     * pace on [Dispatchers.Main.immediate]. Only the most recent progress value
     * ever enters the StateFlow — the rest are discarded at zero allocation cost.
     */
    fun sendFiles(items: List<SendItem>, targets: List<Device>) {
        transferJob?.cancel()

        val primaryName = when (val first = items.firstOrNull()) {
            is SendItem.UriFile  -> first.name ?: "file"
            is SendItem.TextFile -> first.name
            null                 -> "unknown"
        }

        val progressChannel = Channel<Pair<Long, Long>>(capacity = Channel.CONFLATED)

        transferJob = viewModelScope.launch(Dispatchers.IO) {

            _uiState.update { state ->
                state.copy(
                    activeTransfer = TransferState(
                        fileName       = primaryName,
                        progress       = 0f,
                        direction      = TransferDirection.SENDING,
                        transferStatus = "Connecting…",
                    )
                )
            }

            launch(Dispatchers.Main.immediate) {
                progressChannel.receiveAsFlow().collect { (sent, total) ->
                    val ratio = if (total > 0L) sent.toFloat() / total.toFloat() else 0f
                    _uiState.update { state ->
                        state.copy(
                            activeTransfer = state.activeTransfer?.copy(
                                progress = ratio.coerceIn(0f, 1f)
                            )
                        )
                    }
                }
            }

            try {
                OpenAirSender.sendToMany(
                    context    = getApplication(),
                    devices    = targets,
                    items      = items,
                    onStatus   = { status ->
                        _uiState.update { state ->
                            state.copy(
                                activeTransfer = state.activeTransfer?.copy(transferStatus = status)
                            )
                        }
                    },
                    onProgress = { bytesSent, totalBytes ->
                        progressChannel.trySend(bytesSent to totalBytes)
                    }
                )

                _uiState.update { state ->
                    state.copy(
                        activeTransfer = state.activeTransfer?.copy(
                            progress       = 1f,
                            transferStatus = "Transfer complete ✓",
                        )
                    )
                }

            } catch (e: Exception) {
                if (e is kotlinx.coroutines.CancellationException) throw e
                _uiState.update { state ->
                    state.copy(
                        activeTransfer = null,
                        error          = "Transfer failed: ${e.message}",
                    )
                }
            } finally {
                progressChannel.close()
            }
        }
    }

    // ── Received-files history ────────────────────────────────────────────────

    /** Opens a received file in whatever app handles its MIME type. */
    fun openReceivedFile(file: ReceivedFile) {
        val ctx = getApplication<Application>().applicationContext
        val intent = android.content.Intent(android.content.Intent.ACTION_VIEW).apply {
            setDataAndType(Uri.parse(file.mediaUri), file.mimeType)
            addFlags(
                android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION or
                android.content.Intent.FLAG_ACTIVITY_NEW_TASK
            )
        }
        try {
            ctx.startActivity(intent)
        } catch (e: Exception) {
            _uiState.update { it.copy(error = "No app can open ${file.name}") }
        }
    }

    /** Clears the history list (files themselves stay on disk / in MediaStore). */
    fun clearReceivedHistory() {
        _uiState.update { it.copy(receivedFiles = emptyList()) }
        persistReceivedHistory(emptyList())
    }

    private val historyFile: java.io.File
        get() = java.io.File(getApplication<Application>().filesDir, "received_history.json")

    private fun loadReceivedHistory(): List<ReceivedFile> = try {
        if (!historyFile.exists()) emptyList()
        else {
            val arr = org.json.JSONArray(historyFile.readText())
            (0 until arr.length()).mapNotNull { i ->
                val o = arr.optJSONObject(i) ?: return@mapNotNull null
                ReceivedFile(
                    id         = o.optString("id", UUID.randomUUID().toString()),
                    name       = o.optString("name", "file"),
                    sizeLabel  = o.optString("sizeLabel", ""),
                    mediaUri   = o.optString("mediaUri", ""),
                    mimeType   = o.optString("mimeType", "application/octet-stream"),
                    receivedAt = o.optLong("receivedAt", 0L),
                )
            }
        }
    } catch (e: Exception) {
        emptyList()
    }

    private fun persistReceivedHistory(list: List<ReceivedFile>) {
        viewModelScope.launch(Dispatchers.IO) {
            try {
                val arr = org.json.JSONArray()
                list.forEach { rf ->
                    arr.put(org.json.JSONObject().apply {
                        put("id",         rf.id)
                        put("name",       rf.name)
                        put("sizeLabel",  rf.sizeLabel)
                        put("mediaUri",   rf.mediaUri)
                        put("mimeType",   rf.mimeType)
                        put("receivedAt", rf.receivedAt)
                    })
                }
                historyFile.writeText(arr.toString())
            } catch (e: Exception) {
                android.util.Log.w("OpenAirViewModel", "History persist failed: ${e.message}")
            }
        }
    }

    private fun guessMimeType(fileName: String): String {
        val ext = fileName.substringAfterLast('.', "").lowercase()
        if (ext.isEmpty()) return "application/octet-stream"
        return android.webkit.MimeTypeMap.getSingleton()
            .getMimeTypeFromExtension(ext) ?: "application/octet-stream"
    }

    // ── Error handling ────────────────────────────────────────────────────────

    fun dismissError() {
        _uiState.update { it.copy(error = null) }
    }

    // ── ViewModel cleanup ─────────────────────────────────────────────────────

    override fun onCleared() {
        super.onCleared()
        discoveryManager.stopScan()

        if (OpenAirReceiver.isRunning) {
            val ctx = getApplication<Application>().applicationContext
            OpenAirReceiver.stop(ctx)
        }

        incomingDeferred?.cancel()
        incomingDeferred = null
        transferJob = null
    }

    // ── Private helpers ───────────────────────────────────────────────────────

    /**
     * Maps [FileAttachment.id] → [Uri]; kept outside UiState so the state
     * object stays free of Android framework types and trivially testable.
     */
    private val fileUriMap = mutableMapOf<String, Uri>()

    private fun formatBytes(b: Long): String = when {
        b >= 1_048_576L -> "${"%.1f".format(b / 1_048_576.0)} MB"
        b >= 1_024L     -> "${"%.1f".format(b / 1_024.0)} KB"
        else            -> "$b B"
    }
}
