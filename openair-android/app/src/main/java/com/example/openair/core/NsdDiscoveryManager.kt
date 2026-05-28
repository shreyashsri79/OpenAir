package com.example.openair.core

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.util.Log
import java.net.Inet4Address

// ── Domain model ──────────────────────────────────────────────────────────────

/**
 * A fully-resolved peer on the local network.
 *
 * @param name  Human-readable NSD service name (e.g. "Pixel 9 Pro").
 * @param host  Canonical IPv4 address string (e.g. "192.168.1.42").
 * @param port  TCP port the peer is listening on.
 */
data class Device(
    val name: String,
    val host: String,
    val port: Int,
)

// ── Manager ───────────────────────────────────────────────────────────────────

private const val TAG             = "NsdDiscoveryManager"
private const val SERVICE_TYPE    = "_openair._tcp."

/**
 * Control-plane for mDNS-based peer discovery on the local network.
 *
 * This class is completely decoupled from the UI layer. It holds no Compose /
 * LiveData / ViewModel references — only Android's system [NsdManager].
 *
 * ### Typical lifecycle
 * ```
 * val manager = NsdDiscoveryManager(context)
 * manager.startScan(
 *     onStatus = { status -> /* post to UI */ },
 *     onFound  = { device -> /* post to UI */ },
 * )
 * // … when done …
 * manager.stopScan()
 * ```
 *
 * ### Thread safety
 * [NsdManager] delivers callbacks on an internal binder thread, **not** the
 * main thread. Callers are responsible for marshalling [onFound] / [onStatus]
 * to their preferred dispatcher (e.g. `withContext(Dispatchers.Main)`).
 */
class NsdDiscoveryManager(context: Context) {

    // Obtain the system NsdManager once; it is safe to reuse across sessions.
    private val nsdManager: NsdManager =
        context.applicationContext.getSystemService(Context.NSD_SERVICE) as NsdManager

    /**
     * Holds the active [NsdManager.DiscoveryListener] between [startScan] and
     * [stopScan]. `null` when no scan is in progress.
     */
    @Volatile
    private var discoveryListener: NsdManager.DiscoveryListener? = null

    // ── Public API ────────────────────────────────────────────────────────────

    /**
     * Begins mDNS service discovery for [SERVICE_TYPE] peers.
     *
     * If a scan is already in progress it is stopped first so the caller can
     * call [startScan] idempotently (e.g. on ViewModel re-creation).
     *
     * @param onStatus Invoked with a human-readable status string on discovery
     *                 lifecycle events (start, stop, errors). Called on an NSD
     *                 internal thread — marshal to Main if updating UI.
     * @param onFound  Invoked once per successfully *resolved* peer. The
     *                 [Device] is guaranteed to carry a non-blank IPv4 address.
     *                 Called on an NSD internal thread.
     */
    fun startScan(
        onStatus: (String) -> Unit,
        onFound:  (Device) -> Unit,
    ) {
        // Ensure we never register two listeners concurrently.
        stopScan()

        val listener = buildDiscoveryListener(onStatus, onFound)
        discoveryListener = listener

        nsdManager.discoverServices(SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, listener)
        Log.d(TAG, "Discovery started for $SERVICE_TYPE")
    }

    /**
     * Halts an active mDNS discovery session.
     *
     * Silently swallows [IllegalArgumentException] which the OS throws when
     * the listener has already been unregistered — a known NsdManager quirk.
     */
    fun stopScan() {
        discoveryListener?.let { listener ->
            try {
                nsdManager.stopServiceDiscovery(listener)
                Log.d(TAG, "Discovery stopped.")
            } catch (e: IllegalArgumentException) {
                // NsdManager can throw if the listener was already removed by
                // the OS (e.g. network loss). Safe to ignore.
                Log.w(TAG, "stopServiceDiscovery threw (listener already gone): ${e.message}")
            } finally {
                discoveryListener = null
            }
        }
    }

    // ── Private helpers ───────────────────────────────────────────────────────

    /**
     * Builds a [NsdManager.DiscoveryListener] that resolves every matching
     * service to a concrete [Device] via a per-service [NsdManager.ResolveListener].
     */
    private fun buildDiscoveryListener(
        onStatus: (String) -> Unit,
        onFound:  (Device) -> Unit,
    ): NsdManager.DiscoveryListener = object : NsdManager.DiscoveryListener {

        // ── Discovery lifecycle ───────────────────────────────────────────

        override fun onDiscoveryStarted(serviceType: String) {
            Log.d(TAG, "onDiscoveryStarted: $serviceType")
            onStatus("Scanning for OpenAir peers…")
        }

        override fun onDiscoveryStopped(serviceType: String) {
            Log.d(TAG, "onDiscoveryStopped: $serviceType")
            onStatus("Scan stopped.")
        }

        override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {
            Log.e(TAG, "onStartDiscoveryFailed: type=$serviceType code=$errorCode")
            onStatus("Discovery failed to start (error $errorCode).")
            // Null out so stopScan() is a no-op; the OS already cleaned up.
            discoveryListener = null
        }

        override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) {
            Log.e(TAG, "onStopDiscoveryFailed: type=$serviceType code=$errorCode")
            discoveryListener = null
        }

        // ── Service events ────────────────────────────────────────────────

        override fun onServiceFound(service: NsdServiceInfo) {
            Log.d(TAG, "onServiceFound: ${service.serviceName} (${service.serviceType})")

            // Filter to our exact protocol; the OS can surface other types.
            if (service.serviceType.trimEnd('.') != SERVICE_TYPE.trimEnd('.')) {
                Log.d(TAG, "Skipping foreign service type: ${service.serviceType}")
                return
            }

            // Resolution is async and requires a fresh listener per call.
            nsdManager.resolveService(service, buildResolveListener(onFound))
        }

        override fun onServiceLost(service: NsdServiceInfo) {
            // Peer went offline — notify callers if needed in future.
            Log.d(TAG, "onServiceLost: ${service.serviceName}")
        }
    }

    /**
     * Builds a one-shot [NsdManager.ResolveListener] that converts a fully-
     * resolved [NsdServiceInfo] into a [Device] and invokes [onFound].
     *
     * IPv4 extraction: [NsdServiceInfo.host] is an [java.net.InetAddress] which
     * may be an [Inet4Address] or [java.net.Inet6Address].  We only accept IPv4
     * to keep the transport layer simple and predictable.
     */
    private fun buildResolveListener(
        onFound: (Device) -> Unit,
    ): NsdManager.ResolveListener = object : NsdManager.ResolveListener {

        override fun onServiceResolved(resolvedService: NsdServiceInfo) {
            Log.d(TAG, "onServiceResolved: ${resolvedService.serviceName} → ${resolvedService.host}")

            // Safely extract IPv4 address; skip IPv6-only peers.
            val inet4 = resolvedService.host as? Inet4Address
            if (inet4 == null) {
                Log.w(TAG, "Skipping ${resolvedService.serviceName}: no IPv4 address available.")
                return
            }

            val hostAddress = inet4.hostAddress
            if (hostAddress.isNullOrBlank()) {
                Log.w(TAG, "Skipping ${resolvedService.serviceName}: hostAddress is blank.")
                return
            }

            val device = Device(
                name = resolvedService.serviceName,
                host = hostAddress,
                port = resolvedService.port,
            )

            Log.i(TAG, "Peer discovered → $device")
            onFound(device)
        }

        override fun onResolveFailed(serviceInfo: NsdServiceInfo, errorCode: Int) {
            // Resolution failures are transient (e.g. peer just left the LAN).
            Log.w(TAG, "onResolveFailed: ${serviceInfo.serviceName} code=$errorCode")
        }
    }
}