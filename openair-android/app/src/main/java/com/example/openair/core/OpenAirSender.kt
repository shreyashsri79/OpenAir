package com.example.openair.core

import android.content.Context
import android.provider.OpenableColumns
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.BufferedInputStream
import java.io.BufferedOutputStream
import java.io.BufferedReader
import java.io.InputStreamReader
import java.net.Socket
import java.security.MessageDigest

object OpenAirSender {

    suspend fun sendToMany(
        context: Context,
        devices: List<Device>,
        items: List<SendItem>,
        onStatus: (String) -> Unit,
        onProgress: (done: Int, total: Int) -> Unit
    ): Boolean = withContext(Dispatchers.IO) {

        if (devices.isEmpty() || items.isEmpty()) return@withContext false

        val total = devices.size * items.size
        var done = 0

        for (device in devices) {
            for (item in items) {
                onStatus("Sending to ${device.name} (${device.host}:${device.port}) ...")

                val ok = sendSingle(context, device, item, onStatus)
                done += 1
                onProgress(done, total)

                if (!ok) {
                    onStatus("Failed on ${device.name}. Stopping.")
                    return@withContext false
                }
            }
        }

        onStatus("All transfers complete.")
        return@withContext true
    }

    private suspend fun sendSingle(
        context: Context,
        device: Device,
        item: SendItem,
        onStatus: (String) -> Unit
    ): Boolean = withContext(Dispatchers.IO) {
        try {
            when (item) {
                is SendItem.UriFile -> sendUriFile(context, device, item.file, onStatus)
                is SendItem.TextFile -> sendTextFile(device, item, onStatus)
            }
        } catch (e: Exception) {
            onStatus("Error: ${e.message}")
            false
        }
    }

    private fun sendTextFile(
        device: Device,
        item: SendItem.TextFile,
        onStatus: (String) -> Unit
    ): Boolean {
        val bytes = item.bytes
        val sha = sha256OfBytes(bytes)

        Socket(device.host, device.port).use { socket ->
            socket.tcpNoDelay = true

            val out = BufferedOutputStream(socket.getOutputStream())
            val `in` = BufferedReader(InputStreamReader(socket.getInputStream()))

            val header =
                """{"name":"${escapeJson(item.name)}","size":${bytes.size},"sha256":"$sha"}"""

            out.write((header + "\n").toByteArray(Charsets.UTF_8))
            out.flush()

            val resp = `in`.readLine()?.trim() ?: ""
            if (!resp.equals("ACCEPT", ignoreCase = true)) {
                onStatus("Rejected: $resp")
                return false
            }

            out.write(bytes)
            out.flush()
        }

        return true
    }

    private fun sendUriFile(
        context: Context,
        device: Device,
        file: PickedFile,
        onStatus: (String) -> Unit
    ): Boolean {
        val sha = sha256OfUri(context, file.uri)

        Socket(device.host, device.port).use { socket ->
            socket.tcpNoDelay = true

            val out = BufferedOutputStream(socket.getOutputStream())
            val `in` = BufferedReader(InputStreamReader(socket.getInputStream()))

            val header =
                """{"name":"${escapeJson(file.name)}","size":${file.size},"sha256":"$sha"}"""

            out.write((header + "\n").toByteArray(Charsets.UTF_8))
            out.flush()

            val resp = `in`.readLine()?.trim() ?: ""
            if (!resp.equals("ACCEPT", ignoreCase = true)) {
                onStatus("Rejected: $resp")
                return false
            }

            val buffer = ByteArray(64 * 1024)
            var sent = 0L

            context.contentResolver.openInputStream(file.uri).use { raw ->
                if (raw == null) return false
                val input = BufferedInputStream(raw)

                while (true) {
                    val read = input.read(buffer)
                    if (read == -1) break
                    out.write(buffer, 0, read)
                    sent += read.toLong()
                }
            }

            out.flush()

            // If mismatch, receiver will fail hash anyway
            if (sent != file.size) {
                onStatus("Warning: sent $sent bytes, expected ${file.size}")
                return false
            }
        }

        return true
    }

    fun getPickedFiles(context: Context, uris: List<android.net.Uri>): List<PickedFile> {
        return uris.mapNotNull { uri ->
            val cr = context.contentResolver
            val cursor = cr.query(uri, null, null, null, null) ?: return@mapNotNull null
            cursor.use {
                val nameIndex = it.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                val sizeIndex = it.getColumnIndex(OpenableColumns.SIZE)
                if (!it.moveToFirst()) return@mapNotNull null

                val name = if (nameIndex != -1) it.getString(nameIndex) else "file"
                val size = if (sizeIndex != -1) it.getLong(sizeIndex) else -1L
                if (size <= 0) return@mapNotNull null

                PickedFile(uri, name, size)
            }
        }
    }

    private fun sha256OfUri(context: Context, uri: android.net.Uri): String {
        val digest = MessageDigest.getInstance("SHA-256")
        val buffer = ByteArray(64 * 1024)

        context.contentResolver.openInputStream(uri).use { raw ->
            requireNotNull(raw) { "Cannot open file stream" }
            val input = BufferedInputStream(raw)

            while (true) {
                val read = input.read(buffer)
                if (read == -1) break
                digest.update(buffer, 0, read)
            }
        }

        return digest.digest().joinToString("") { "%02x".format(it) }
    }

    private fun sha256OfBytes(bytes: ByteArray): String {
        val digest = MessageDigest.getInstance("SHA-256")
        digest.update(bytes)
        return digest.digest().joinToString("") { "%02x".format(it) }
    }

    private fun escapeJson(s: String): String {
        return s.replace("\\", "\\\\").replace("\"", "\\\"")
    }
}
