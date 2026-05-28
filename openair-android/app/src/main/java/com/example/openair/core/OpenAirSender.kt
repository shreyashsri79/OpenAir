package com.example.openair.core

import android.content.Context
import android.net.Uri
import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.channels.ReceiveChannel
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.io.BufferedReader
import java.io.FileInputStream
import java.io.InputStreamReader
import java.io.OutputStreamWriter
import java.io.PrintWriter
import java.net.InetSocketAddress
import java.net.Socket
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.nio.channels.FileChannel
import java.nio.channels.SocketChannel
import java.util.concurrent.atomic.AtomicLong
import kotlin.math.min

// ─────────────────────────────────────────────────────────────────────────────
// Send item model (unchanged)
// ─────────────────────────────────────────────────────────────────────────────

sealed class SendItem {
    data class UriFile(val uri: Uri, val name: String? = null) : SendItem()
    data class TextFile(val name: String, val bytes: ByteArray) : SendItem()
}

// ─────────────────────────────────────────────────────────────────────────────
// Core engine
// ─────────────────────────────────────────────────────────────────────────────

object OpenAirSender {

    private const val TAG           = "OpenAirSender"
    private const val MAX_RETRY     = 3
    private const val WORKERS       = 50

    // Fixed chunk size: 512 KB is the sweet spot for LAN/WiFi.
    // - Small enough that 4 workers always have work queued (no starvation).
    // - Large enough that header overhead is negligible (<0.003%).
    // - At 80 Mbps, one chunk = ~51ms — pipeline stays full.
    // BDP-based sizing was removed: the RTT measurement via handshake
    // round-trip is dominated by JVM/socket setup overhead and produces
    // wildly inaccurate BDP values on sub-100ms LANs.
    private const val CHUNK_SIZE    = 2L * 1024 * 1024   // 512 KB

    private const val HANDSHAKE_TIMEOUT_MS = 5_000

    // Socket send buffer: 4 MB. Tells the kernel to buffer 4 MB of outbound
    // data per connection so writers don't block waiting for ACKs.
    private const val SOCKET_SEND_BUF = 4 * 1024 * 1024

    // ── Internal models ───────────────────────────────────────────────────────

    private data class Chunk(val id: Int, val offset: Long, val size: Long, var retry: Int = 0)

    private data class StreamerConfig(
        val addr      : InetSocketAddress,
        val workers   : Int,
        val chunkSize : Long
    )

    // ── Public API ────────────────────────────────────────────────────────────

    suspend fun sendToMany(
        context    : Context,
        devices    : List<Device>,
        items      : List<SendItem>,
        onStatus   : (String) -> Unit,
        onProgress : (Long, Long) -> Unit
    ): Boolean = withContext(Dispatchers.IO) {
        var allSuccessful = true
        for (device in devices) {
            for (item in items) {
                val success = when (item) {
                    is SendItem.UriFile  -> sendUri(context, device, item, onStatus, onProgress)
                    is SendItem.TextFile -> sendText(device, item, onStatus, onProgress)
                }
                if (!success) allSuccessful = false
            }
        }
        allSuccessful
    }

    // ── Per-item dispatch ─────────────────────────────────────────────────────

    private suspend fun sendUri(
        context    : Context,
        device     : Device,
        item       : SendItem.UriFile,
        onStatus   : (String) -> Unit,
        onProgress : (Long, Long) -> Unit
    ): Boolean {
        val pfd = context.contentResolver.openFileDescriptor(item.uri, "r")
            ?: run { onStatus("Cannot open file: ${item.name ?: item.uri}"); return false }
        return try {
            val name = item.name ?: item.uri.lastPathSegment ?: "file.dat"
            sendFileDescriptor(context, device, pfd.fileDescriptor, name, onStatus, onProgress)
        } finally {
            pfd.close()
        }
    }

    private suspend fun sendText(
        device     : Device,
        item       : SendItem.TextFile,
        onStatus   : (String) -> Unit,
        onProgress : (Long, Long) -> Unit
    ): Boolean = withContext(Dispatchers.IO) {
        val tmp = kotlin.io.path.createTempFile(prefix = "oa_text_", suffix = ".txt").toFile()
        return@withContext try {
            tmp.writeBytes(item.bytes)
            val fd = java.io.FileInputStream(tmp).fd
            sendFileDescriptor(null, device, fd, item.name, onStatus, onProgress)
        } finally {
            tmp.delete()
        }
    }

    // ── Core transfer pipeline ────────────────────────────────────────────────

    /**
     * Opens the file descriptor fresh per-worker so each worker gets its own
     * kernel file-description with an independent file offset and page-cache
     * position — no internal lock contention between workers on the same fd.
     *
     * We pass the raw [java.io.FileDescriptor] rather than a [FileChannel] so
     * workers can each call [FileInputStream(fd).channel] independently.
     * Note: on Android, opening multiple FileInputStream on the same fd is
     * safe for read-only positional access (pread under the hood).
     */
    private suspend fun sendFileDescriptor(
        context    : Context?,
        device     : Device,
        fd         : java.io.FileDescriptor,
        fileName   : String,
        onStatus   : (String) -> Unit,
        onProgress : (Long, Long) -> Unit
    ): Boolean = coroutineScope {

        val addr     = InetSocketAddress(device.host, device.port)
        // Measure file size once via a temporary channel; close it immediately.
        val fileSize = FileInputStream(fd).channel.use { it.size() }

        return@coroutineScope try {
            onStatus("Connecting to ${device.name}…")
            val accepted = handshake(addr, fileName, fileSize, onStatus)
            if (!accepted) return@coroutineScope false

            val cfg = StreamerConfig(addr, WORKERS, CHUNK_SIZE)
            Log.d(TAG, "chunk=${CHUNK_SIZE.toHumanBytes()} workers=$WORKERS fileSize=${fileSize.toHumanBytes()}")

            val jobsChannel = Channel<Chunk>(Channel.UNLIMITED)
            enqueueChunks(fileSize, CHUNK_SIZE, jobsChannel)

            val bytesSent = AtomicLong(0)
            onStatus("Streaming $fileName…")

            val monitorJob = launch(Dispatchers.IO) {
                while (isActive) {
                    onProgress(bytesSent.get(), fileSize)
                    delay(80)
                    if (bytesSent.get() >= fileSize) break
                }
            }

            val ok = runWorkerPool(fd, jobsChannel, cfg, bytesSent)
            monitorJob.cancel()

            if (ok) {
                onProgress(fileSize, fileSize)
                onStatus("Transfer complete ✓")
            } else {
                onStatus("Transfer failed — a chunk exceeded $MAX_RETRY retries.")
            }
            ok

        } catch (e: Exception) {
            if (e is kotlinx.coroutines.CancellationException) throw e
            Log.e(TAG, "sendFileDescriptor failed", e)
            onStatus("Error: ${e.message}")
            false
        }
    }

    // ── Handshake ─────────────────────────────────────────────────────────────

    /**
     * Simplified handshake — we no longer use the RTT/BW values for chunk
     * sizing, so this just confirms the receiver accepted the transfer.
     */
    private fun handshake(
        addr     : InetSocketAddress,
        name     : String,
        size     : Long,
        onStatus : (String) -> Unit
    ): Boolean {
        Socket().use { sock ->
            sock.connect(addr, HANDSHAKE_TIMEOUT_MS)
            val reader = BufferedReader(InputStreamReader(sock.getInputStream()))
            val writer = PrintWriter(OutputStreamWriter(sock.getOutputStream()), true)

            val meta = JSONObject().apply {
                put("name",      name)
                put("size",      size)
                put("timestamp", System.currentTimeMillis() / 1000)
            }
            writer.println(meta.toString())

            val accept = reader.readLine()
            if (accept == null || !accept.uppercase().contains("ACCEPT")) {
                onStatus("Receiver rejected: $accept")
                return false
            }

            // Drain the probe ACK line (receiver sends it; we no longer use it).
            reader.readLine()

            Log.d(TAG, "Handshake accepted for $name")
            return true
        }
    }

    // ── Job queue ─────────────────────────────────────────────────────────────

    private fun enqueueChunks(fileSize: Long, chunkSize: Long, jobs: Channel<Chunk>) {
        var offset  = 0L
        var counter = 0
        while (offset < fileSize) {
            val sliceSize = min(fileSize - offset, chunkSize)
            jobs.trySend(Chunk(counter, offset, sliceSize))
            offset  += sliceSize
            counter += 1
        }
        jobs.close()
        Log.d(TAG, "Enqueued $counter chunks of ${chunkSize.toHumanBytes()}")
    }

    // ── Worker pool ───────────────────────────────────────────────────────────

    private suspend fun runWorkerPool(
        fd        : java.io.FileDescriptor,
        jobs      : Channel<Chunk>,
        cfg       : StreamerConfig,
        bytesSent : AtomicLong
    ): Boolean = coroutineScope {
        val retryChannel = Channel<Chunk>(capacity = 50)

        val workers = (0 until cfg.workers).map {
            async(Dispatchers.IO) {
                // Each worker opens its own FileInputStream → FileChannel.
                // This gives it a private kernel file-description (independent
                // offset tracking, no lock contention with sibling workers).
                val fis = FileInputStream(fd)
                val fc  = fis.channel
                try {
                    worker(fc, jobs, retryChannel, cfg, bytesSent)
                } finally {
                    fc.close()
                    fis.close()
                }
            }
        }

        return@coroutineScope try {
            workers.awaitAll()
            true
        } catch (e: Exception) {
            if (e is kotlinx.coroutines.CancellationException) throw e
            Log.e(TAG, "Worker pool failed: ${e.message}")
            false
        } finally {
            retryChannel.close()
        }
    }

    // ── Per-worker loop ───────────────────────────────────────────────────────

    private suspend fun worker(
        fileChannel  : FileChannel,
        jobs         : ReceiveChannel<Chunk>,
        retryChannel : Channel<Chunk>,
        cfg          : StreamerConfig,
        bytesSent    : AtomicLong
    ) {
        val conn = SocketChannel.open()
        conn.socket().sendBufferSize = SOCKET_SEND_BUF
        conn.connect(cfg.addr)

        // 12-byte header buffer: [offset: Long LE][size: Int LE]
        val headerBuf  = ByteBuffer.allocateDirect(12).order(ByteOrder.LITTLE_ENDIAN)
        // Payload buffer sized to chunk. Direct buffer = no JVM heap copy on write.
        val payloadBuf = ByteBuffer.allocateDirect(cfg.chunkSize.toInt())

        try {
            while (currentCoroutineContext().isActive) {
                val chunk = retryChannel.tryReceive().getOrNull()
                    ?: jobs.receiveCatching().getOrNull()
                    ?: break

                try {
                    sendChunk(conn, fileChannel, headerBuf, payloadBuf, chunk, bytesSent)
                } catch (e: Exception) {
                    if (e is kotlinx.coroutines.CancellationException) throw e
                    chunk.retry++
                    Log.w(TAG, "Chunk ${chunk.id} failed (attempt ${chunk.retry}): ${e.message}")
                    if (chunk.retry > MAX_RETRY) {
                        throw Exception("Chunk ${chunk.id} exceeded max retries ($MAX_RETRY)")
                    }
                    retryChannel.send(chunk)
                }
            }
        } finally {
            conn.close()
        }
    }

    // ── Single chunk send ─────────────────────────────────────────────────────

    /**
     * Wire format per chunk (little-endian binary):
     * ```
     * [ 8 bytes: file offset (Long) ]
     * [ 4 bytes: payload size  (Int) ]
     * [ N bytes: raw payload        ]
     * ```
     *
     * Header and payload are written as a **gather write** via
     * [SocketChannel.write(Array<ByteBuffer>)] — a single syscall that
     * wraps `writev(2)`. This halves syscall count vs writing header then
     * payload separately.
     */
    private fun sendChunk(
        conn       : SocketChannel,
        fileChannel: FileChannel,
        headerBuf  : ByteBuffer,
        payloadBuf : ByteBuffer,
        chunk      : Chunk,
        bytesSent  : AtomicLong
    ) {
        // 1. Read chunk from disk into direct buffer (positional read — no seek needed)
        payloadBuf.clear()
        payloadBuf.limit(chunk.size.toInt())
        var read = 0
        while (read < chunk.size) {
            val n = fileChannel.read(payloadBuf, chunk.offset + read)
            if (n == -1) break
            read += n
        }
        payloadBuf.flip()

        // 2. Build header
        headerBuf.clear()
        headerBuf.putLong(chunk.offset)
        headerBuf.putInt(read)
        headerBuf.flip()

        // 3. Gather write: header + payload in one writev() syscall
        val bufs = arrayOf(headerBuf, payloadBuf)
        var totalToWrite = headerBuf.remaining().toLong() + payloadBuf.remaining().toLong()
        while (totalToWrite > 0) {
            totalToWrite -= conn.write(bufs)
        }

        bytesSent.addAndGet(read.toLong())
    }

    // ── Formatting helpers ────────────────────────────────────────────────────

    private fun Long.toHumanBytes(): String = when {
        this >= 1_048_576L -> "${"%.1f".format(this / 1_048_576.0)} MB"
        this >= 1_024L     -> "${"%.1f".format(this / 1_024.0)} KB"
        else               -> "$this B"
    }
}