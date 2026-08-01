package com.example.openair.v2

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeoutOrNull
import mobile.Clipboard
import mobile.ClipboardCallback
import mobile.Discovery
import mobile.PeerInfo
import mobile.Receiver

/**
 * ReceiveSession owns the listening half of the core for the whole process.
 *
 * It exists for the same reason `openaird` does on the desktop (BUILD-PLAN.md
 * M4): receiving must not depend on a screen being open. The activity comes and
 * goes; this and the foreground service hosting it do not, so a paired device
 * can send to this phone while the app is in the background.
 *
 * Android has no IPC here, unlike the desktop. D-31 puts the same Go core
 * in-process via gomobile, so the "daemon" is a service in this process and the
 * UI talks to it through a state flow rather than a socket.
 */
object ReceiveSession {

    /** How long an inbound offer waits for a human before it is declined. */
    private const val PROMPT_TIMEOUT_MS = 60_000L

    data class State(
        val running: Boolean = false,
        val listenAddr: String = "",
        val port: Int = 0,
        val devices: List<DeviceRow> = emptyList(),
        val incoming: TransferState = TransferState(),
        val lastClipboard: String = "",
        val lastClipboardFrom: String = "",
        val status: String = "",
    )

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state.asStateFlow()

    /** The offer waiting on an answer, or null. Whoever answers first wins. */
    private val _prompt = MutableStateFlow<OfferPrompt?>(null)
    val prompt: StateFlow<OfferPrompt?> = _prompt.asStateFlow()

    private var receiver: Receiver? = null
    private var discovery: Discovery? = null
    private var clipboard: Clipboard? = null
    private var appContext: Context? = null

    /** Set by the service so a prompt with no UI attached still reaches a user. */
    @Volatile
    var onOfferPrompt: ((OfferPrompt) -> Unit)? = null

    /** Set by the service so received clipboard content can be shown. */
    @Volatile
    var onClipboard: ((peer: String, text: String) -> Unit)? = null

    val isRunning: Boolean get() = receiver != null

    /**
     * Starts listening and announcing. Safe to call twice; the second call is a
     * no-op rather than a second listener.
     *
     * Blocking: binds the socket. Call it off the main thread.
     */
    @Synchronized
    fun start(context: Context, displayName: String): Result<Unit> {
        if (receiver != null) return Result.success(Unit)
        appContext = context.applicationContext

        val repo = CoreRepository.get(context)
        val clip = Clipboard(repo.identity, displayName).apply {
            setClipboardCallback(object : ClipboardCallback {
                override fun onClipboard(peer: PeerInfo, text: String) = deliverClipboard(peer, text)
            })
        }

        val r = repo.newReceiver(
            displayName = displayName,
            onOffer = ::promptForOffer,
            onProgress = { id, done, total ->
                _state.update {
                    it.copy(incoming = TransferState(id, done, total.coerceAtLeast(0), active = true))
                }
            },
            onComplete = { id, ok ->
                _state.update {
                    it.copy(
                        incoming = it.incoming.copy(transferId = id, active = false),
                        status = if (ok) "received" else "transfer failed",
                    )
                }
            },
        )
        r.setClipboard(clip)

        return runCatching { r.start(":0") }
            .onSuccess {
                receiver = r
                clipboard = clip
                _state.update {
                    it.copy(running = true, listenAddr = r.addr(), port = r.port().toInt(), status = "listening")
                }
                restartDiscovery(displayName, r.port().toInt())
            }
            .onFailure { e -> _state.update { it.copy(status = "listen failed: ${e.message}") } }
            .map { }
    }

    @Synchronized
    fun stop() {
        val r = receiver ?: return
        receiver = null
        clipboard = null
        runCatching { r.stop() }
        runCatching { discovery?.stop() }
        discovery = null
        _prompt.value?.answer?.complete(false)
        _prompt.value = null
        _state.update { it.copy(running = false, listenAddr = "", port = 0, status = "stopped") }
    }

    /** The Clipboard bound to this device, for pushing as well as receiving. */
    @Synchronized
    fun clipboardFor(context: Context, displayName: String): Clipboard =
        clipboard ?: Clipboard(CoreRepository.get(context).identity, displayName).also { clipboard = it }

    // ── discovery ─────────────────────────────────────────────────────────────

    @Synchronized
    fun restartDiscovery(displayName: String, listeningPort: Int) {
        runCatching { discovery?.stop() }
        val ctx = appContext ?: return
        val d = CoreRepository.get(ctx).newDiscovery(displayName, listeningPort)
        runCatching { d.start() }
            .onSuccess { discovery = d }
            .onFailure { e -> _state.update { it.copy(status = "discovery unavailable: ${e.message}") } }
    }

    /** Reads the current candidate set. Blocking; call it off the main thread. */
    fun peers(): List<DeviceRow> {
        val d = discovery ?: return emptyList()
        val identity = appContext?.let { CoreRepository.get(it).identity }
        val list = d.peers()
        return (0 until list.len().toInt()).map { i ->
            val deviceId = list.deviceID(i.toLong())
            val paired = list.paired(i.toLong())
            DeviceRow(
                deviceId = deviceId,
                fingerprint = list.fingerprint(i.toLong()),
                displayName = list.displayName(i.toLong()),
                addr = list.addr(i.toLong()),
                via = list.via(i.toLong()),
                paired = paired,
                // Trust level and unlock state are read here rather than in the
                // UI because both are the core's answer, not the shell's: what a
                // device may do is trust-store state, and whether it is unlocked
                // right now is a six-hour timer inside Go (§6, D-30).
                level = if (paired && identity != null) identity.trustLevel(deviceId).toInt() else 0,
                unlockedUntil = if (paired && identity != null) identity.unlockedUntil(deviceId) else 0L,
            )
        }
    }

    fun publishDevices(devices: List<DeviceRow>) = _state.update { it.copy(devices = devices) }

    // ── prompts ───────────────────────────────────────────────────────────────

    /**
     * Called on a Go goroutine, and it blocks that goroutine until answered:
     * the offer verifier is a synchronous decision and the sender is held open
     * for its duration.
     *
     * Unanswered means declined, never accepted. The same rule the daemon
     * follows (D-53): a device that accepted files because nobody looked at the
     * screen would be the worse default, and the sender gets a refusal rather
     * than an indefinite wait.
     */
    private fun promptForOffer(peer: PeerInfo, offer: mobile.Offer): Boolean {
        val prompt = OfferPrompt(
            peerFingerprint = peer.fingerprint(),
            peerName = peer.displayName(),
            fileCount = offer.fileCount().toInt(),
            totalBytes = offer.totalBytes(),
            firstPath = if (offer.fileCount() > 0) offer.path(0) else "",
            answer = CompletableDeferred(),
        )
        _prompt.value = prompt
        onOfferPrompt?.invoke(prompt)

        val answered = runBlocking {
            withTimeoutOrNull(PROMPT_TIMEOUT_MS) { prompt.answer.await() }
        } ?: false

        _prompt.compareAndSet(prompt, null)
        return answered
    }

    fun answerOffer(accept: Boolean) {
        val prompt = _prompt.value ?: return
        prompt.answer.complete(accept)
        _prompt.value = null
        _state.update { it.copy(status = if (accept) "accepting" else "declined") }
    }

    // ── clipboard ─────────────────────────────────────────────────────────────

    /**
     * Applies received clipboard content, and reports it either way.
     *
     * The write is attempted and not depended on: from Android 10 the system
     * ignores clipboard writes from a process that is not in the foreground and
     * does not report an error, so a push that arrives while the app is in the
     * background is silently dropped. The notification the service posts is what
     * makes it reachable regardless, which is why the text is published here
     * rather than only pasted.
     */
    private fun deliverClipboard(peer: PeerInfo, text: String) {
        _state.update {
            it.copy(lastClipboard = text, lastClipboardFrom = peer.fingerprint(), status = "clipboard received")
        }
        appContext?.let { ctx ->
            runCatching {
                val cm = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
                cm.setPrimaryClip(ClipData.newPlainText("OpenAir", text))
            }
        }
        onClipboard?.invoke(peer.fingerprint(), text)
    }
}
