package com.example.openair.core

import android.annotation.SuppressLint
import android.content.ContentValues
import android.content.Context
import android.net.Uri
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Build
import android.os.ParcelFileDescriptor
import android.provider.MediaStore
import android.util.Log
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStream
import java.io.InputStreamReader
import java.net.ServerSocket
import java.net.Socket
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.nio.channels.FileChannel
import java.util.concurrent.atomic.AtomicLong

// ─────────────────────────────────────────────────────────────────────────────
// OpenAirReceiver — TCP server + mDNS advertisement for the Android side.
//
// Design mirrors the Go `receiver.go`:
//   • One control connection  → handshake  (JSON metadata, ACCEPT/REJECT)
//   • N data connections      → parallel chunk writes via FileChannel.write(buf, pos)
//     FileChannel.write(ByteBuffer, long) is thread-safe and maps to pwrite(2)
//     on Android/Linux — concurrent positional writes to non-overlapping regions
//     are safe without any mutex.
//
// MediaStore integration:
//   • IS_PENDING=1 during transfer  → file invisible in gallery until complete
//   • IS_PENDING=0 on completion    → file appears in Downloads
//   • Requires API 29+; for API 26-28 we fall back to getExternalFilesDir.
// ─────────────────────────────────────────────────────────────────────────────

object OpenAirReceiver {

    private const val TAG              = "OpenAirReceiver"
    const  val PORT                    = 9000
    private const val SERVICE_TYPE     = "_openair._tcp."
    private const val SERVICE_NAME     = "OpenAir-Android"
    private const val ACCEPT_TIMEOUT   = 30_000L          // ms before auto-reject
    private const val MAX_CHUNK_BYTES  = 4 * 1024 * 1024  // 4 MB — must match sender

    // ── Internal models ───────────────────────────────────────────────────────

    private data class FileMeta(
        val name      : String,
        val size      : Long,
        val timestamp : Long,
    )

    /**
     * Holds live state for one in-flight incoming transfer.
     * Created in [handleControl] after ACCEPT; read by every concurrent
     * [handleData] goroutine that follows.
     */
    private class ReceiverSession(
        val meta          : FileMeta,
        val fileChannel   : FileChannel,
        val mediaStoreUri : Uri,
        val pfd           : ParcelFileDescriptor,
        val bytesReceived : AtomicLong = AtomicLong(0),
    )

    // ── Lifecycle state ───────────────────────────────────────────────────────

    @Volatile private var appContext      : Context?                      = null
    @Volatile private var serverSocket    : ServerSocket?                  = null
    @Volatile private var nsdListener     : NsdManager.RegistrationListener? = null
    @Volatile private var session         : ReceiverSession?               = null
    private val  sessionLock              = Any()

    /** Deferred owned by the receiver; resolved by ViewModel on user action. */
    @Volatile private var pendingDeferred : CompletableDeferred<Boolean>?  = null

    private var scope : CoroutineScope? = null

    // ── Callbacks set by ViewModel ────────────────────────────────────────────

    /**
     * Called when a control connection arrives.
     * ViewModel must:
     *   1. Post an [IncomingRequest] to UiState.
     *   2. Create + return a [CompletableDeferred<Boolean>].
     * The receiver suspends on that deferred (up to [ACCEPT_TIMEOUT] ms).
     */
    var onIncoming  : ((fileName: String, sizeLabel: String) -> CompletableDeferred<Boolean>)? = null
    var onProgress  : ((bytesReceived: Long, total: Long) -> Unit)?                             = null
    var onComplete  : ((fileName: String, uri: Uri) -> Unit)?                                   = null
    var onError     : ((message: String) -> Unit)?                                              = null

    // ── Public API ────────────────────────────────────────────────────────────

    val isRunning: Boolean get() = serverSocket?.isClosed == false

    fun start(context: Context) {
        if (isRunning) return

        appContext = context.applicationContext

        val job = SupervisorJob()
        val scp = CoroutineScope(Dispatchers.IO + job)
        scope = scp

        scp.launch {
            try {
                val ss = ServerSocket(PORT)
                serverSocket = ss
                registerMdns(context.applicationContext)
                Log.i(TAG, "Listening on :$PORT")

                while (isActive) {
                    try {
                        val conn = ss.accept()
                        // Each connection (control or data) gets its own coroutine.
                        launch { handleConnection(conn) }
                    } catch (e: Exception) {
                        if (!isActive) break          // ServerSocket.close() → accept() throws
                        Log.w(TAG, "accept() error: ${e.message}")
                    }
                }
            } catch (e: Exception) {
                Log.e(TAG, "Server loop failed: ${e.message}")
                onError?.invoke("Receiver failed: ${e.message}")
            }
        }
    }

    fun stop(context: Context) {
        unregisterMdns(context.applicationContext)
        serverSocket?.close()
        serverSocket = null
        scope?.cancel()
        scope = null
        synchronized(sessionLock) { session = null }
        pendingDeferred?.cancel()
        pendingDeferred = null
        appContext = null
    }

    /**
     * Called by ViewModel when the user taps Accept or Reject on the incoming
     * transfer dialog.  [accept] = true → ACCEPT; false → REJECT.
     */
    fun resolveIncoming(accept: Boolean) {
        pendingDeferred?.complete(accept)
    }

    // ── mDNS registration ─────────────────────────────────────────────────────

    private fun registerMdns(context: Context) {
        val nsd  = context.getSystemService(Context.NSD_SERVICE) as NsdManager
        val info = NsdServiceInfo().apply {
            serviceName = SERVICE_NAME
            serviceType = SERVICE_TYPE
            port        = PORT
        }
        val listener = object : NsdManager.RegistrationListener {
            override fun onServiceRegistered(si: NsdServiceInfo) {
                Log.i(TAG, "mDNS registered: ${si.serviceName}")
            }
            override fun onRegistrationFailed(si: NsdServiceInfo, code: Int) {
                Log.e(TAG, "mDNS registration failed: $code")
                onError?.invoke("mDNS registration failed (code $code)")
            }
            override fun onServiceUnregistered(si: NsdServiceInfo) {
                Log.i(TAG, "mDNS unregistered")
            }
            override fun onUnregistrationFailed(si: NsdServiceInfo, code: Int) {
                Log.w(TAG, "mDNS unregistration failed: $code")
            }
        }
        nsdListener = listener
        nsd.registerService(info, NsdManager.PROTOCOL_DNS_SD, listener)
    }

    private fun unregisterMdns(context: Context) {
        nsdListener?.let { listener ->
            try {
                val nsd = context.getSystemService(Context.NSD_SERVICE) as NsdManager
                nsd.unregisterService(listener)
            } catch (e: Exception) {
                Log.w(TAG, "mDNS unregister error: ${e.message}")
            } finally {
                nsdListener = null
            }
        }
    }

    // ── Connection router ─────────────────────────────────────────────────────

    /**
     * Peeks the first byte without consuming it.
     * '{' (0x7B) → control (JSON metadata)
     * anything else → data (binary chunk stream)
     */
    private suspend fun handleConnection(conn: Socket) {
        conn.use {
            val raw = conn.getInputStream()

            val peeked = ByteArray(1)
            if (raw.read(peeked) <= 0) return

            // Wrap the raw stream so the peeked byte is re-delivered to handlers.
            val stream = PeekInputStream(peeked[0], raw)

            if (peeked[0] == '{'.code.toByte()) {
                handleControl(conn, stream)
            } else {
                handleData(stream)
            }
        }
    }

    // ── Control handler ───────────────────────────────────────────────────────

    private suspend fun handleControl(conn: Socket, stream: InputStream) {
        val reader = BufferedReader(InputStreamReader(stream))
        val out    = conn.getOutputStream()

        val line = reader.readLine() ?: return
        val json = runCatching { JSONObject(line) }.getOrElse {
            Log.e(TAG, "Bad JSON in control: $line"); return
        }

        val meta = FileMeta(
            name      = json.optString("name", "received_file"),
            size      = json.optLong("size", 0L),
            timestamp = json.optLong("timestamp", 0L),
        )
        Log.i(TAG, "Incoming: '${meta.name}' (${formatBytes(meta.size)})")

        if (meta.size <= 0L) {
            out.write("REJECT\n".toByteArray()); out.flush()
            return
        }

        // ── Ask user (suspend until accept/reject or timeout) ─────────────────
        val deferred = onIncoming?.invoke(meta.name, formatBytes(meta.size))
            ?: CompletableDeferred<Boolean>().also { it.complete(false) }
        pendingDeferred = deferred

        val accepted = withTimeoutOrNull(ACCEPT_TIMEOUT) { deferred.await() } ?: false
        pendingDeferred = null

        if (!accepted) {
            Log.i(TAG, "Transfer rejected by user (or timed out)")
            out.write("REJECT\n".toByteArray()); out.flush()
            return
        }

        // ── Open MediaStore file ──────────────────────────────────────────────
        val ctx = appContext ?: run {
            out.write("REJECT\n".toByteArray()); out.flush(); return
        }
        val msFile = createMediaStoreFile(ctx, meta.name) ?: run {
            Log.e(TAG, "MediaStore file creation failed")
            onError?.invoke("Cannot create output file")
            out.write("REJECT\n".toByteArray()); out.flush()
            return
        }

        // Pre-allocate the full file so concurrent pwrite(2) calls never need to
        // grow the file — identical to Go's f.Truncate(meta.size).
        msFile.fileChannel.truncate(meta.size)

        val newSession = ReceiverSession(
            meta          = meta,
            fileChannel   = msFile.fileChannel,
            mediaStoreUri = msFile.uri,
            pfd           = msFile.pfd,
        )
        synchronized(sessionLock) { session = newSession }

        // ── Send ACCEPT + probe ACK ───────────────────────────────────────────
        out.write("ACCEPT\n".toByteArray())
        val ack = JSONObject().apply {
            put("timestamp", System.currentTimeMillis() / 1000L)
            put("data",      "ACK:${meta.size}")
        }
        out.write("${ack}\n".toByteArray())
        out.flush()

        Log.i(TAG, "Handshake done — data workers may connect")
    }

    // ── Data handler ──────────────────────────────────────────────────────────

    /**
     * Reads a stream of binary chunks:
     * ```
     * [ 8 bytes : file offset (Long, little-endian) ]
     * [ 4 bytes : payload size (Int, little-endian)  ]
     * [ N bytes : raw payload                        ]
     * ```
     *
     * Each call runs in its own coroutine (one per data connection), so multiple
     * workers write concurrently.  [FileChannel.write(ByteBuffer, long)] is
     * thread-safe per Java spec and maps to [pwrite(2)] on Android/Linux —
     * concurrent positional writes to non-overlapping offsets are atomic.
     */
    private fun handleData(stream: InputStream) {
        val xfr = synchronized(sessionLock) { session }
        if (xfr == null) {
            Log.w(TAG, "Data connection before handshake — dropping")
            return
        }
        Log.d(TAG, "Data worker connected (${xfr.meta.name})")

        val headerBuf = ByteArray(12)
        val chunkBuf  = ByteArray(MAX_CHUNK_BYTES)

        while (true) {
            // 1. Read 12-byte header
            if (!readFull(stream, headerBuf, 12)) break

            val bb     = ByteBuffer.wrap(headerBuf).order(ByteOrder.LITTLE_ENDIAN)
            val offset = bb.long         // bytes 0–7
            val size   = bb.int          // bytes 8–11

            if (size <= 0 || size > MAX_CHUNK_BYTES) {
                Log.e(TAG, "Invalid chunk size: $size"); break
            }

            // 2. Read payload
            if (!readFull(stream, chunkBuf, size)) break

            // 3. Positional write — no mutex needed (pwrite-safe)
            val payloadBuf = ByteBuffer.wrap(chunkBuf, 0, size)
            var pos        = offset
            while (payloadBuf.hasRemaining()) {
                pos += xfr.fileChannel.write(payloadBuf, pos)
            }

            // 4. Progress
            val received = xfr.bytesReceived.addAndGet(size.toLong())
            onProgress?.invoke(received, xfr.meta.size)

            // 5. Done?
            if (received >= xfr.meta.size) {
                finishTransfer(xfr)
                break
            }
        }
    }

    private fun finishTransfer(xfr: ReceiverSession) {
        try {
            xfr.fileChannel.force(true)   // fsync
            xfr.fileChannel.close()
            xfr.pfd.close()
        } catch (e: Exception) {
            Log.w(TAG, "Close error: ${e.message}")
        }

        // Mark IS_PENDING = 0 so the file is visible in the system Downloads app.
        appContext?.let { ctx: Context ->
            markMediaStoreComplete(ctx, xfr.mediaStoreUri)
        }

        synchronized(sessionLock) {
            if (session === xfr) session = null
        }

        Log.i(TAG, "Transfer complete: ${xfr.meta.name}")
        onComplete?.invoke(xfr.meta.name, xfr.mediaStoreUri)
    }

    // ── MediaStore helpers ────────────────────────────────────────────────────

    private data class MediaStoreFile(
        val fileChannel : FileChannel,
        val pfd         : ParcelFileDescriptor,
        val uri         : Uri,
    )

    @SuppressLint("InlinedApi")
    private fun createMediaStoreFile(context: Context, fileName: String): MediaStoreFile? {
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            // API 29+ — write to MediaStore Downloads (visible in Files app).
            val resolver = context.contentResolver
            val values   = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, fileName)
                put(MediaStore.Downloads.MIME_TYPE,    "application/octet-stream")
                put(MediaStore.Downloads.IS_PENDING,   1)
            }
            val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
                ?: return null

            val pfd = resolver.openFileDescriptor(uri, "rw") ?: run {
                resolver.delete(uri, null, null)
                return null
            }
            // FileOutputStream(fd).channel → FileChannelImpl backed by the same
            // file descriptor.  write(ByteBuffer, long) → pwrite(2).
            val channel = java.io.FileOutputStream(pfd.fileDescriptor).channel
            MediaStoreFile(channel, pfd, uri)

        } else {
            // API 26-28 fallback — app's external Downloads directory.
            val dir = context.getExternalFilesDir(android.os.Environment.DIRECTORY_DOWNLOADS)
                ?: return null
            val file = java.io.File(dir, fileName)
            val pfd  = ParcelFileDescriptor.open(
                file,
                ParcelFileDescriptor.MODE_READ_WRITE or
                ParcelFileDescriptor.MODE_CREATE or
                ParcelFileDescriptor.MODE_TRUNCATE
            )
            val channel = java.io.FileOutputStream(pfd.fileDescriptor).channel
            MediaStoreFile(channel, pfd, Uri.fromFile(file))
        }
    }

    @SuppressLint("InlinedApi")
    fun markMediaStoreComplete(context: Context, uri: Uri) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            runCatching {
                val values = ContentValues().apply {
                    put(MediaStore.Downloads.IS_PENDING, 0)
                }
                context.contentResolver.update(uri, values, null, null)
            }
        }
        // For API <29 the file is already directly on disk; nothing to mark.
    }

    // ── I/O helpers ───────────────────────────────────────────────────────────

    /** Reads exactly [length] bytes into [buf]; returns false on EOF or error. */
    private fun readFull(stream: InputStream, buf: ByteArray, length: Int): Boolean {
        var offset = 0
        while (offset < length) {
            val n = stream.read(buf, offset, length - offset)
            if (n == -1) return false
            offset += n
        }
        return true
    }

    private fun formatBytes(b: Long): String = when {
        b >= 1_048_576L -> "${"%.1f".format(b / 1_048_576.0)} MB"
        b >= 1_024L     -> "${"%.1f".format(b / 1_024.0)} KB"
        else            -> "$b B"
    }

    // ── PeekInputStream ───────────────────────────────────────────────────────

    /**
     * Wraps an [InputStream] and prepends one byte that was already read
     * (used for routing without consuming the first byte).
     */
    private class PeekInputStream(
        private val peeked  : Byte,
        private val wrapped : InputStream,
    ) : InputStream() {

        private var peekedConsumed = false

        override fun read(): Int {
            if (!peekedConsumed) {
                peekedConsumed = true
                return peeked.toInt() and 0xFF
            }
            return wrapped.read()
        }

        override fun read(b: ByteArray, off: Int, len: Int): Int {
            if (len == 0) return 0
            if (!peekedConsumed) {
                peekedConsumed = true
                b[off] = peeked
                if (len == 1) return 1
                val n = wrapped.read(b, off + 1, len - 1)
                return if (n == -1) 1 else 1 + n
            }
            return wrapped.read(b, off, len)
        }

        override fun close() = wrapped.close()
    }
}
