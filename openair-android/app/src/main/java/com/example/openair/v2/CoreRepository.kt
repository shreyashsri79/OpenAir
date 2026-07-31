package com.example.openair.v2

import android.content.Context
import kotlinx.coroutines.CompletableDeferred
import mobile.Discovery
import mobile.FileList
import mobile.Identity
import mobile.Mobile
import mobile.Offer
import mobile.OfferVerifier
import mobile.Pairing
import mobile.PeerInfo
import mobile.ProgressCallback
import mobile.Receiver
import mobile.SASVerifier
import mobile.Sender
import mobile.TransferCallback
import java.io.File

/**
 * CoreRepository owns the gomobile-bound OpenAir core for the whole process.
 *
 * The v2 stack is the same Go code the desktop CLI runs, bound with gomobile
 * (D-10, D-31). Nothing in this package reimplements the protocol; it drives
 * the binding and turns its callbacks into something Compose can render.
 *
 * Threading, because the binding is explicit about it: every call that blocks
 * -- SendFiles, AwaitPeer, PairWithOffer -- must run off the main thread, and
 * every callback arrives on a Go goroutine rather than the main looper. The
 * view model is what moves values between the two.
 */
class CoreRepository private constructor(
    val identity: Identity,
    private val downloadDir: File,
) {
    companion object {
        @Volatile
        private var instance: CoreRepository? = null

        /**
         * Opens (or creates) this device's keys in app-private storage.
         *
         * filesDir is the right home for them until the keystore tier lands
         * (D-21): it is private to this app, survives updates, and is removed
         * when the user uninstalls, which is the behaviour a device identity
         * should have.
         */
        fun get(context: Context): CoreRepository =
            instance ?: synchronized(this) {
                instance ?: build(context.applicationContext).also { instance = it }
            }

        private fun build(context: Context): CoreRepository {
            val keyDir = File(context.filesDir, "openair").apply { mkdirs() }
            val identity = Mobile.loadIdentity(keyDir.absolutePath)
            val downloads = File(
                context.getExternalFilesDir(null) ?: context.filesDir,
                "OpenAir",
            ).apply { mkdirs() }
            return CoreRepository(identity, downloads)
        }
    }

    /** This device's identifier, grouped for a human to compare. */
    val fingerprint: String get() = identity.fingerprint()

    /** Where received files land. Shown in the UI so a user can find them. */
    val destination: String get() = downloadDir.absolutePath

    // gomobile maps Go's int to Java long, so every count and index crossing the
    // boundary arrives as a Long and is narrowed here rather than at each call.
    fun pairedCount(): Int = identity.pairedCount().toInt()

    fun isPaired(deviceId: String): Boolean = identity.isPaired(deviceId)

    fun unpair(deviceId: String) = identity.unpair(deviceId)

    // ── receiving ─────────────────────────────────────────────────────────────

    /**
     * Builds the receiver. The offer prompt is the only thing left for the user
     * to answer: an unpaired device never reaches it, because the trust store
     * refuses the session before any transfer is offered.
     */
    fun newReceiver(
        displayName: String,
        onOffer: (PeerInfo, Offer) -> Boolean,
        onProgress: (String, Long, Long) -> Unit,
        onComplete: (String, Boolean) -> Unit,
    ): Receiver = Receiver(identity, displayName, downloadDir.absolutePath).apply {
        setOfferVerifier(object : OfferVerifier {
            override fun verifyOffer(peer: PeerInfo, offer: Offer): Boolean = onOffer(peer, offer)
        })
        setProgressCallback(object : ProgressCallback {
            override fun onProgress(transferId: String, done: Long, total: Long) =
                onProgress(transferId, done, total)
        })
        setTransferCallback(object : TransferCallback {
            override fun onComplete(transferId: String, ok: Boolean) = onComplete(transferId, ok)
        })
    }

    // ── sending ───────────────────────────────────────────────────────────────

    fun newSender(
        displayName: String,
        onProgress: (String, Long, Long) -> Unit,
    ): Sender = Sender(identity, displayName).apply {
        setProgressCallback(object : ProgressCallback {
            override fun onProgress(transferId: String, done: Long, total: Long) =
                onProgress(transferId, done, total)
        })
    }

    fun newFileList(): FileList = Mobile.newFileList()

    // ── discovery ─────────────────────────────────────────────────────────────

    /**
     * @param listeningPort the port the receiver bound, or 0 to browse without
     * announcing. Announcing a port nothing is listening on publishes an
     * address that refuses every connection made to it (D-48).
     */
    fun newDiscovery(displayName: String, listeningPort: Int): Discovery =
        Discovery(identity, displayName, listeningPort.toLong())

    // ── pairing ───────────────────────────────────────────────────────────────

    /**
     * Builds the pairing driver.
     *
     * [confirmSas] is handed the six digits and must not return until the user
     * has answered: the comparison is the entire security of pairing, and the
     * binding blocks the exchange until it does (PROTOCOL.md §5.2). It is
     * called on a Go goroutine, which is why the view model bridges it through
     * a CompletableDeferred rather than touching Compose state directly.
     */
    fun newPairing(
        displayName: String,
        confirmSas: (String, PeerInfo) -> Boolean,
    ): Pairing = Pairing(identity, displayName).apply {
        setSASVerifier(object : SASVerifier {
            override fun confirmSAS(sas: String, peer: PeerInfo): Boolean = confirmSas(sas, peer)
        })
    }
}

/** A prompt waiting on the user, bridged from a Go goroutine to Compose. */
class PendingPrompt<T>(val answer: CompletableDeferred<T> = CompletableDeferred())
