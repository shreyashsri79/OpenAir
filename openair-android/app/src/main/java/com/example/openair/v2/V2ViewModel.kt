package com.example.openair.v2

import android.app.Application
import android.content.ClipboardManager
import android.content.Context
import android.net.Uri
import android.provider.OpenableColumns
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import mobile.Pairing
import java.io.File

/** One device as the UI shows it. */
data class DeviceRow(
    val deviceId: String,
    val fingerprint: String,
    val displayName: String,
    val addr: String,
    val via: String,
    val paired: Boolean,
    /** 0 unpaired, 1 trusted, 2 owned (PROTOCOL.md §6). */
    val level: Int = 0,
    /** 0 locked, -1 always-on, otherwise the unix millisecond expiry. */
    val unlockedUntil: Long = 0,
) {
    val owned: Boolean get() = level >= 2
    val unlocked: Boolean get() = unlockedUntil != 0L
}

/** A file the user picked to send. */
data class PickedFile(val name: String, val path: String, val bytes: Long)

/** The digits both users compare, waiting on an answer. */
data class SasPrompt(
    val digits: String,
    val peerFingerprint: String,
    val peerName: String,
    val answer: CompletableDeferred<Boolean>,
)

/** An inbound transfer waiting on an accept. */
data class OfferPrompt(
    val peerFingerprint: String,
    val peerName: String,
    val fileCount: Int,
    val totalBytes: Long,
    val firstPath: String,
    val answer: CompletableDeferred<Boolean>,
)

data class TransferState(
    val transferId: String = "",
    val done: Long = 0,
    val total: Long = 0,
    val active: Boolean = false,
) {
    val fraction: Float get() = if (total > 0) (done.toFloat() / total).coerceIn(0f, 1f) else 0f
}

data class V2UiState(
    val fingerprint: String = "",
    val displayName: String = "",
    val destination: String = "",
    val receiving: Boolean = false,
    val listenAddr: String = "",
    val devices: List<DeviceRow> = emptyList(),
    val picked: List<PickedFile> = emptyList(),
    val pairedCount: Int = 0,
    val offerToShow: String? = null,
    val offerGrouped: String = "",
    val sasPrompt: SasPrompt? = null,
    val offerPrompt: OfferPrompt? = null,
    val sending: TransferState = TransferState(),
    val incoming: TransferState = TransferState(),
    /** D-21's tier for this device: 0 none, 1 passphrase, 2 keystore. */
    val protectionTier: Int = 0,
    val needsScreenLock: Boolean = false,
    val clipboardText: String = "",
    val lastClipboard: String = "",
    val lastClipboardFrom: String = "",
    val status: String = "",
)

/**
 * V2ViewModel drives the gomobile core and exposes it as Compose state.
 *
 * Every blocking call goes to Dispatchers.IO, because the binding's contract is
 * that SendFiles, AwaitPeer and PairWithOffer occupy their thread for the whole
 * operation. Every callback arrives on a Go goroutine, so it hands work back
 * through the state flow rather than touching Compose directly.
 */
class V2ViewModel(app: Application) : AndroidViewModel(app) {

    private val repo = CoreRepository.get(app)
    private val _state = MutableStateFlow(V2UiState())
    val state: StateFlow<V2UiState> = _state.asStateFlow()

    private var pairing: Pairing? = null
    private var pollJob: Job? = null

    init {
        _state.update {
            it.copy(
                fingerprint = repo.fingerprint,
                displayName = android.os.Build.MODEL ?: "android",
                destination = repo.destination,
                pairedCount = repo.pairedCount(),
                protectionTier = repo.identity.protectionTier().toInt(),
                needsScreenLock = !PrivilegeKeystore.isDeviceSecure(app),
                status = "ready",
            )
        }
        // The listening half lives in ReceiveSession, held by a foreground
        // service, so it survives this view model -- M4's rule applied to a
        // process that has no daemon to talk to. Everything here mirrors it.
        viewModelScope.launch {
            ReceiveSession.state.collect { s ->
                _state.update {
                    it.copy(
                        receiving = s.running,
                        listenAddr = s.listenAddr,
                        devices = s.devices.ifEmpty { it.devices },
                        incoming = s.incoming,
                        lastClipboard = s.lastClipboard,
                        lastClipboardFrom = s.lastClipboardFrom,
                        status = s.status.ifEmpty { it.status },
                    )
                }
            }
        }
        viewModelScope.launch {
            ReceiveSession.prompt.collect { p -> _state.update { it.copy(offerPrompt = p) } }
        }
    }

    // ── receiving ─────────────────────────────────────────────────────────────

    /**
     * Starts the foreground service that holds the listener.
     *
     * Not the listener itself: a receiver owned by this view model dies with the
     * activity, and then the device is reachable only while someone is looking
     * at it. The service is what makes "send a file to my phone" work with the
     * phone in a pocket.
     */
    fun startReceiving() {
        if (_state.value.receiving) return
        ReceiverService.start(getApplication(), _state.value.displayName)
        _state.update { it.copy(status = "starting") }
    }

    fun stopReceiving() {
        ReceiverService.stop(getApplication())
        _state.update { it.copy(status = "stopping") }
    }

    /** Answers the offer prompt. The service's notification answers the same one. */
    fun answerOffer(accept: Boolean) = ReceiveSession.answerOffer(accept)

    // ── discovery ─────────────────────────────────────────────────────────────

    /**
     * Browses without announcing while nothing is listening (D-48). Once the
     * service is up it owns discovery, and this only reads what it publishes.
     */
    fun startDiscovery() {
        if (pollJob != null) return
        if (!ReceiveSession.isRunning) {
            viewModelScope.launch {
                withContext(Dispatchers.IO) {
                    ReceiveSession.restartDiscovery(_state.value.displayName, 0)
                }
            }
        }
        pollJob = viewModelScope.launch { pollDevices() }
    }

    /**
     * Polls rather than subscribing: gobind cannot carry a channel across the
     * boundary, and a list that refreshes twice a second is what a device
     * picker wants anyway.
     */
    private suspend fun pollDevices() {
        while (true) {
            val snapshot = withContext(Dispatchers.IO) {
                runCatching { ReceiveSession.peers() }.getOrDefault(emptyList())
            }
            _state.update { it.copy(devices = snapshot, pairedCount = repo.pairedCount()) }
            delay(500)
        }
    }

    // ── pairing ───────────────────────────────────────────────────────────────

    /** Shows this device's offer and waits for someone to scan or type it. */
    fun showPairingOffer() {
        val p = pairing ?: repo.newPairing(_state.value.displayName, ::promptForSas).also { pairing = it }
        viewModelScope.launch {
            runCatching { withContext(Dispatchers.IO) { p.showOffer("") } }
                .onSuccess { offer ->
                    _state.update {
                        it.copy(offerToShow = offer, offerGrouped = p.offerGrouped(), status = "waiting to pair")
                    }
                    val peer = runCatching { withContext(Dispatchers.IO) { p.awaitPeer() } }
                    _state.update {
                        it.copy(
                            offerToShow = null,
                            offerGrouped = "",
                            pairedCount = repo.pairedCount(),
                            status = peer.fold(
                                { "paired with ${it.fingerprint()}" },
                                { e -> "pairing failed: ${e.message}" },
                            ),
                        )
                    }
                    withContext(Dispatchers.IO) { runCatching { p.stop() } }
                    pairing = null
                }
                .onFailure { e -> _state.update { it.copy(status = "cannot show a code: ${e.message}") } }
        }
    }

    fun cancelPairingOffer() {
        val p = pairing ?: return
        pairing = null
        viewModelScope.launch {
            withContext(Dispatchers.IO) { runCatching { p.stop() } }
            _state.update { it.copy(offerToShow = null, offerGrouped = "", status = "pairing cancelled") }
        }
    }

    /** Pairs with a device whose code was scanned or typed here. */
    fun pairWithCode(code: String) {
        val p = repo.newPairing(_state.value.displayName, ::promptForSas)
        viewModelScope.launch {
            _state.update { it.copy(status = "pairing...") }
            val peer = runCatching { withContext(Dispatchers.IO) { p.pairWithOffer(code) } }
            _state.update {
                it.copy(
                    pairedCount = repo.pairedCount(),
                    status = peer.fold(
                        { "paired with ${it.fingerprint()}" },
                        { e -> "pairing failed: ${e.message}" },
                    ),
                )
            }
        }
    }

    /**
     * Called from a Go goroutine, and blocks it until the user answers. That is
     * the contract: §5.2 has no timeout short of the exchange's own, and no way
     * to skip the comparison.
     */
    private fun promptForSas(digits: String, peer: mobile.PeerInfo): Boolean {
        val prompt = SasPrompt(
            digits = digits,
            peerFingerprint = peer.fingerprint(),
            peerName = peer.displayName(),
            answer = CompletableDeferred(),
        )
        _state.update { it.copy(sasPrompt = prompt) }
        return kotlinx.coroutines.runBlocking { prompt.answer.await() }
    }

    fun answerSas(matches: Boolean) {
        val prompt = _state.value.sasPrompt ?: return
        prompt.answer.complete(matches)
        _state.update { it.copy(sasPrompt = null) }
    }

    fun unpair(deviceId: String) {
        runCatching { repo.unpair(deviceId) }
        _state.update { it.copy(pairedCount = repo.pairedCount(), status = "unpaired") }
    }

    // ── owned access (M6) ─────────────────────────────────────────────────────

    /**
     * Set when the platform wants the user to authenticate before it will
     * release the key-encryption key. The activity watches this, runs the
     * device-credential prompt, and calls [onAuthenticated].
     *
     * It is a request rather than a call because only an Activity can launch the
     * credential intent, and a view model that held one would leak it.
     */
    private val _authNeeded = MutableStateFlow(false)
    val authNeeded: StateFlow<Boolean> = _authNeeded.asStateFlow()

    private var pendingAuthAction: (() -> Unit)? = null

    /**
     * Creates this device's privilege key, sealed by the Android Keystore
     * (D-21 tier 1). One-time, and the credential prompt is part of it: the
     * Keystore will not use a user-authentication key before the user has
     * authenticated, which is the property being bought.
     */
    fun protect() {
        if (!PrivilegeKeystore.isDeviceSecure(getApplication())) {
            _state.update {
                it.copy(status = "set a screen lock first: without one this device cannot protect a privilege key")
            }
            return
        }
        withAuthentication {
            val kek = PrivilegeKeystore.createKek(getApplication())
            repo.identity.protect(kek)
            java.util.Arrays.fill(kek, 0)
            _state.update {
                it.copy(
                    protectionTier = repo.identity.protectionTier().toInt(),
                    status = "owned access set up; pair your devices again so they learn this key",
                )
            }
        }
    }

    /**
     * Starts a six-hour owned session for one device (D-18, D-30).
     *
     * Per device, not per app: the confirmation the user just gave names the
     * machine it authorises, and reaching a second one asks again.
     */
    fun unlock(device: DeviceRow) {
        withAuthentication {
            val kek = PrivilegeKeystore.unlockKek(getApplication())
            val expiry = repo.identity.unlock(device.deviceId, kek, "", false, 0L)
            java.util.Arrays.fill(kek, 0)
            _state.update {
                it.copy(status = if (expiry == 0L) "${device.displayName} unlocked" else "${device.displayName} unlocked for six hours")
            }
            refreshTrust()
        }
    }

    /** Ends every owned session and wipes the decrypted key. */
    fun lockAll() {
        repo.identity.lock()
        _state.update { it.copy(status = "owned access locked") }
        refreshTrust()
    }

    /**
     * Grants or withdraws a paired device's unattended access (§6.4).
     *
     * A local act on this device, never a response to a request: a peer cannot
     * ask to be promoted (PRD R3).
     */
    fun setOwned(device: DeviceRow, owned: Boolean) {
        viewModelScope.launch(Dispatchers.IO) {
            runCatching { repo.identity.setOwned(device.deviceId, owned) }
                .onSuccess {
                    _state.update {
                        it.copy(status = if (owned) "${device.displayName} can now act unattended" else "${device.displayName} is trusted only")
                    }
                    refreshTrust()
                }
                .onFailure { e -> _state.update { it.copy(status = e.message ?: "could not change trust level") } }
        }
    }

    /** Called by the activity once the credential prompt has finished. */
    fun onAuthenticated(ok: Boolean) {
        val action = pendingAuthAction
        pendingAuthAction = null
        _authNeeded.value = false
        if (!ok) {
            _state.update { it.copy(status = "not unlocked: authentication was cancelled") }
            return
        }
        action?.let { run(it) }
    }

    /**
     * Runs a block that needs the Keystore, deferring it behind a credential
     * prompt when the platform says the user must authenticate first.
     */
    private fun withAuthentication(block: () -> Unit) {
        viewModelScope.launch(Dispatchers.IO) { run(block) }
    }

    private fun run(block: () -> Unit) {
        try {
            block()
        } catch (e: PrivilegeKeystore.NeedsAuthentication) {
            pendingAuthAction = block
            _authNeeded.value = true
        } catch (e: PrivilegeKeystore.NeedsReprotect) {
            // The screen lock changed, so the Keystore key is gone and the
            // sealed privilege key can no longer be opened by anyone, including
            // us. Say so rather than retrying: the way out is to set owned
            // access up again.
            _state.update { it.copy(status = e.message ?: "owned access must be set up again") }
        } catch (e: Exception) {
            _state.update { it.copy(status = e.message ?: "owned access failed") }
        }
    }

    /** Re-reads trust level and unlock state for the devices on screen. */
    private fun refreshTrust() {
        _state.update { st ->
            st.copy(
                devices = st.devices.map { row ->
                    if (!row.paired) row else row.copy(
                        level = repo.identity.trustLevel(row.deviceId).toInt(),
                        unlockedUntil = repo.identity.unlockedUntil(row.deviceId),
                    )
                },
            )
        }
    }

    // ── sending ───────────────────────────────────────────────────────────────

    /**
     * Copies a picked document into app-private storage and adds it to the
     * outgoing list.
     *
     * The copy is not laziness: a content:// URI is not a file path, and the Go
     * core takes paths. Streaming the URI through the binding would mean a
     * second transfer API that exists only for Android.
     */
    fun addFile(uri: Uri) {
        viewModelScope.launch {
            val picked = withContext(Dispatchers.IO) { copyIn(uri) }
            if (picked == null) {
                _state.update { it.copy(status = "could not read that file") }
                return@launch
            }
            _state.update { it.copy(picked = it.picked + picked) }
        }
    }

    fun clearFiles() = _state.update { it.copy(picked = emptyList()) }

    private fun copyIn(uri: Uri): PickedFile? {
        val resolver = getApplication<Application>().contentResolver
        var name = "file"
        runCatching {
            resolver.query(uri, null, null, null, null)?.use { c ->
                val i = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                if (i >= 0 && c.moveToFirst()) name = c.getString(i)
            }
        }
        val outDir = File(getApplication<Application>().cacheDir, "outgoing").apply { mkdirs() }
        val out = File(outDir, name)
        return runCatching {
            resolver.openInputStream(uri).use { input ->
                requireNotNull(input)
                out.outputStream().use { input.copyTo(it) }
            }
            PickedFile(name = name, path = out.absolutePath, bytes = out.length())
        }.getOrNull()
    }

    fun sendTo(device: DeviceRow) {
        val files = _state.value.picked
        if (files.isEmpty()) {
            _state.update { it.copy(status = "pick a file first") }
            return
        }
        if (!device.paired) {
            _state.update { it.copy(status = "pair with ${device.fingerprint} first") }
            return
        }

        viewModelScope.launch {
            _state.update { it.copy(sending = TransferState(active = true), status = "sending") }
            val result = withContext(Dispatchers.IO) {
                runCatching {
                    val list = repo.newFileList()
                    files.forEach { list.add(it.path, it.name) }
                    val sender = repo.newSender(_state.value.displayName) { id, done, total ->
                        _state.update {
                            it.copy(sending = TransferState(id, done, total, active = true))
                        }
                    }
                    sender.sendFiles(device.addr, list)
                }
            }
            _state.update {
                it.copy(
                    sending = it.sending.copy(active = false),
                    status = result.fold({ "sent" }, { e -> "send failed: ${e.message}" }),
                )
            }
        }
    }

    // ── clipboard ─────────────────────────────────────────────────────────────

    fun setClipboardText(text: String) = _state.update { it.copy(clipboardText = text) }

    /**
     * Pushes text to a paired device (§9, M5).
     *
     * With the field left empty it sends what is on this phone's clipboard. That
     * read has to happen while the app is in the foreground -- from Android 10
     * the system refuses it otherwise -- which is exactly where a button press
     * puts us.
     */
    fun pushClipboard(device: DeviceRow) {
        if (!device.paired) {
            _state.update { it.copy(status = "pair with ${device.fingerprint} first") }
            return
        }
        val typed = _state.value.clipboardText
        val text = typed.ifEmpty { readSystemClipboard() }
        if (text.isEmpty()) {
            _state.update { it.copy(status = "nothing to push: type something or copy it first") }
            return
        }

        viewModelScope.launch {
            _state.update { it.copy(status = "pushing clipboard") }
            val result = withContext(Dispatchers.IO) {
                runCatching {
                    ReceiveSession
                        .clipboardFor(getApplication(), _state.value.displayName)
                        .push(device.addr, text)
                }
            }
            _state.update {
                it.copy(status = result.fold({ "clipboard sent" }, { e -> "clipboard failed: ${e.message}" }))
            }
        }
    }

    private fun readSystemClipboard(): String {
        val cm = getApplication<Application>()
            .getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager ?: return ""
        val clip = cm.primaryClip ?: return ""
        if (clip.itemCount == 0) return ""
        return clip.getItemAt(0).coerceToText(getApplication()).toString()
    }

    override fun onCleared() {
        // The receiver deliberately outlives this: it belongs to the service.
        pollJob?.cancel()
        runCatching { pairing?.stop() }
        super.onCleared()
    }
}
