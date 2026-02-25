package com.example.openair.core


import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import java.net.Inet4Address

class NsdDiscoveryManager(private val context: Context) {

    private val nsd = context.getSystemService(Context.NSD_SERVICE) as NsdManager
    private var discoveryListener: NsdManager.DiscoveryListener? = null
    private val wifiManager = context.applicationContext.getSystemService(Context.WIFI_SERVICE) as android.net.wifi.WifiManager
    private var multicastLock: android.net.wifi.WifiManager.MulticastLock? = null

    fun startScan(
        onStatus: (String) -> Unit,
        onFound: (Device) -> Unit
    ) {
        stopScan()

        // Acquiring the multicastlock to enable hotspot transfer
        multicastLock = wifiManager.createMulticastLock("openair_lock").apply {
            setReferenceCounted(true)
            acquire()
        }

        val listener = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(serviceType: String) {
                onStatus("Scanning...")
            }

            override fun onServiceFound(serviceInfo: NsdServiceInfo) {
                // Filter: only _openair._tcp
                if (!serviceInfo.serviceType.contains("_openair._tcp")) return

                nsd.resolveService(serviceInfo, object : NsdManager.ResolveListener {
                    override fun onResolveFailed(serviceInfo: NsdServiceInfo, errorCode: Int) {
                        // ignore
                    }

                    override fun onServiceResolved(resolved: NsdServiceInfo) {
                        val host = resolved.host ?: return
                        val port = resolved.port.takeIf { it > 0 } ?: 8080

                        // Prefer IPv4
                        val finalHost =
                            if (host is Inet4Address) host.hostAddress
                            else host.hostAddress

                        onFound(
                            Device(
                                name = resolved.serviceName,
                                host = finalHost,
                                port = port
                            )
                        )
                    }
                })
            }

            override fun onServiceLost(serviceInfo: NsdServiceInfo) {
                // optional
            }

            override fun onDiscoveryStopped(serviceType: String) {
                onStatus("Scan stopped.")
            }

            override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {
                onStatus("Scan failed (code=$errorCode)")
                try { nsd.stopServiceDiscovery(this) } catch (_: Exception) {}
            }

            override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) {
                onStatus("Stop failed (code=$errorCode)")
                try { nsd.stopServiceDiscovery(this) } catch (_: Exception) {}
            }
        }

        discoveryListener = listener
        nsd.discoverServices("_openair._tcp.", NsdManager.PROTOCOL_DNS_SD, listener)
    }

    fun stopScan() {
        multicastLock?.let {
            if (it.isHeld) it.release()
        }
        val listener = discoveryListener ?: return
        try {
            nsd.stopServiceDiscovery(listener)
        } catch (_: Exception) {
        }
        discoveryListener = null
    }
}
