package com.example.openair.core


import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Environment
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream
import java.net.ServerSocket
import java.net.Socket
import kotlin.concurrent.thread

class OpenAirReceiverManager(private val context: Context) {
    private var serverSocket: ServerSocket? = null
    private var isRunning = false
    private val nsdManager = context.getSystemService(Context.NSD_SERVICE) as NsdManager
    private var registrationListener: NsdManager.RegistrationListener? = null

    fun startReceiver(onProgress: (Long, Long) -> Unit, onStatus: (String) -> Unit) {
        isRunning = true
        thread {
            try {
                serverSocket = ServerSocket(0) // 0 picks an available port
                val port = serverSocket!!.localPort

                // 1. Register via mDNS
                registerService(port)

                while (isRunning) {
                    val client = serverSocket?.accept() ?: break
                    handleClient(client, onProgress, onStatus)
                }
            } catch (e: Exception) {
                onStatus("Error: ${e.message}")
            }
        }
    }

    private fun handleClient(socket: Socket, onProgress: (Long, Long) -> Unit, onStatus: (String) -> Unit) {
        thread {
            socket.use { s ->
                val reader = s.getInputStream().bufferedReader()
                val writer = s.getOutputStream().bufferedWriter()

                // 1. Read JSON Header
                val headerJson = reader.readLine() ?: return@thread
                val meta = JSONObject(headerJson)
                val fileName = meta.getString("name")
                val fileSize = meta.getLong("size")

                onStatus("Receiving: $fileName")

                // 2. Send ACCEPT
                writer.write("ACCEPT\n")
                writer.flush()

                // 3. Receive File Data
                val downloadsDir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)
                val destFile = File(downloadsDir, fileName)

                s.getInputStream().use { input ->
                    FileOutputStream(destFile).use { output ->
                        val buffer = ByteArray(8192)
                        var bytesRead: Int
                        var totalRead = 0L

                        while (input.read(buffer).also { bytesRead = it } != -1) {
                            output.write(buffer, 0, bytesRead)
                            totalRead += bytesRead
                            onProgress(totalRead, fileSize)
                        }
                    }
                }
                onStatus("Saved to Downloads: $fileName")
            }
        }
    }

    private fun registerService(port: Int) {
        val serviceInfo = NsdServiceInfo().apply {
            serviceName = "OpenAir-${android.os.Build.MODEL}"
            serviceType = "_openair._tcp"
            setPort(port)
        }

        registrationListener = object : NsdManager.RegistrationListener {
            override fun onServiceRegistered(info: NsdServiceInfo) {}
            override fun onRegistrationFailed(info: NsdServiceInfo, err: Int) {}
            override fun onServiceUnregistered(info: NsdServiceInfo) {}
            override fun onUnregistrationFailed(info: NsdServiceInfo, err: Int) {}
        }
        nsdManager.registerService(serviceInfo, NsdManager.PROTOCOL_DNS_SD, registrationListener)
    }

    fun stopReceiver() {
        isRunning = false
        serverSocket?.close()
        registrationListener?.let { nsdManager.unregisterService(it) }
    }
}