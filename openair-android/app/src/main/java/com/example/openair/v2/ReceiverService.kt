package com.example.openair.v2

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * ReceiverService keeps this device reachable while no screen is open.
 *
 * It is the Android half of M4. The desktop runs `openaird`; Android has no IPC
 * because D-31 puts the same Go core in this process, so the equivalent is a
 * foreground service holding the listener and the announcement. Without it the
 * phone stops receiving the moment the activity is destroyed, and "send a file
 * to my phone" would mean "unlock the phone, open the app, then send".
 *
 * The notification is not decoration: a foreground service must show one, and
 * this one carries the two things a user needs while the app is closed -- the
 * accept/decline choice for an inbound transfer, and any clipboard content that
 * arrived while the system was refusing background clipboard writes.
 */
class ReceiverService : Service() {

    companion object {
        private const val CHANNEL_STATUS = "openair_v2_status"
        private const val CHANNEL_ALERTS = "openair_v2_alerts"
        private const val NOTIF_ONGOING = 4201
        private const val NOTIF_OFFER = 4202
        private const val NOTIF_CLIP = 4203

        const val ACTION_START = "com.example.openair.v2.START"
        const val ACTION_STOP = "com.example.openair.v2.STOP"
        const val ACTION_ACCEPT = "com.example.openair.v2.ACCEPT"
        const val ACTION_DECLINE = "com.example.openair.v2.DECLINE"
        const val EXTRA_NAME = "displayName"

        fun start(context: Context, displayName: String) {
            val intent = Intent(context, ReceiverService::class.java)
                .setAction(ACTION_START)
                .putExtra(EXTRA_NAME, displayName)
            context.startForegroundService(intent)
        }

        fun stop(context: Context) {
            context.startService(Intent(context, ReceiverService::class.java).setAction(ACTION_STOP))
        }
    }

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var displayName: String = Build.MODEL ?: "android"

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        createChannels()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                ReceiveSession.stop()
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return START_NOT_STICKY
            }
            ACTION_ACCEPT -> {
                ReceiveSession.answerOffer(true)
                NotificationManagerCompat.from(this).cancel(NOTIF_OFFER)
                return START_STICKY
            }
            ACTION_DECLINE -> {
                ReceiveSession.answerOffer(false)
                NotificationManagerCompat.from(this).cancel(NOTIF_OFFER)
                return START_STICKY
            }
        }

        intent?.getStringExtra(EXTRA_NAME)?.let { displayName = it }

        // The notification must be up before anything blocks, or the system
        // kills the service for starting foreground too slowly.
        startForegroundCompat(ongoingNotification("starting"))

        ReceiveSession.onOfferPrompt = ::postOfferNotification
        ReceiveSession.onClipboard = ::postClipboardNotification

        scope.launch {
            ReceiveSession.start(applicationContext, displayName)
                .onFailure { e -> updateOngoing("not listening: ${e.message}") }
                .onSuccess {
                    updateOngoing("listening on ${ReceiveSession.state.value.listenAddr}")
                    pollDevices()
                }
        }
        return START_STICKY
    }

    /**
     * Refreshes the device list while the service runs, so the UI has something
     * to show the moment it attaches. gobind cannot carry a channel, so this is
     * a poll (the same one the view model used to run for itself).
     */
    private suspend fun pollDevices() {
        while (ReceiveSession.isRunning) {
            ReceiveSession.publishDevices(runCatching { ReceiveSession.peers() }.getOrDefault(emptyList()))
            delay(1000)
        }
    }

    override fun onDestroy() {
        ReceiveSession.onOfferPrompt = null
        ReceiveSession.onClipboard = null
        ReceiveSession.stop()
        scope.cancel()
        super.onDestroy()
    }

    // ── notifications ─────────────────────────────────────────────────────────

    private fun createChannels() {
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_STATUS, "OpenAir receiving", NotificationManager.IMPORTANCE_LOW).apply {
                description = "Shown while this device is reachable by paired devices."
            },
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_ALERTS, "OpenAir requests", NotificationManager.IMPORTANCE_HIGH).apply {
                description = "Incoming transfers and clipboard content from paired devices."
            },
        )
    }

    private fun startForegroundCompat(n: Notification) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(NOTIF_ONGOING, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            startForeground(NOTIF_ONGOING, n)
        }
    }

    private fun ongoingNotification(text: String): Notification =
        NotificationCompat.Builder(this, CHANNEL_STATUS)
            .setContentTitle("OpenAir 2")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_upload_done)
            .setOngoing(true)
            .setContentIntent(openAppIntent())
            .addAction(0, "Stop", serviceAction(ACTION_STOP))
            .build()

    private fun updateOngoing(text: String) {
        // POST_NOTIFICATIONS may have been refused; the service still runs, and
        // the system shows its own minimal notice for it.
        runCatching {
            NotificationManagerCompat.from(this).notify(NOTIF_ONGOING, ongoingNotification(text))
        }
    }

    /**
     * Puts an inbound offer in front of the user when no screen is showing it.
     *
     * The two actions answer the same CompletableDeferred the in-app dialog
     * does, so whichever the user reaches first decides, and the other becomes a
     * no-op rather than a second answer.
     */
    private fun postOfferNotification(prompt: OfferPrompt) {
        val who = prompt.peerName.ifEmpty { prompt.peerFingerprint }
        val n = NotificationCompat.Builder(this, CHANNEL_ALERTS)
            .setContentTitle("$who wants to send ${prompt.fileCount} file(s)")
            .setContentText(prompt.firstPath)
            .setSmallIcon(android.R.drawable.stat_sys_download)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setAutoCancel(true)
            .setContentIntent(openAppIntent())
            .addAction(0, "Accept", serviceAction(ACTION_ACCEPT))
            .addAction(0, "Decline", serviceAction(ACTION_DECLINE))
            .build()
        runCatching { NotificationManagerCompat.from(this).notify(NOTIF_OFFER, n) }
    }

    /**
     * Shows clipboard content that arrived.
     *
     * From Android 10 the system ignores a clipboard write from a background
     * process, silently. The paste is attempted anyway for the case where the
     * app is in front; this notification is what makes the content reachable
     * when it is not.
     */
    private fun postClipboardNotification(peer: String, text: String) {
        val n = NotificationCompat.Builder(this, CHANNEL_ALERTS)
            .setContentTitle("Clipboard from $peer")
            .setContentText(text.take(120))
            .setStyle(NotificationCompat.BigTextStyle().bigText(text.take(2000)))
            .setSmallIcon(android.R.drawable.ic_menu_edit)
            .setAutoCancel(true)
            .setContentIntent(openAppIntent())
            .build()
        runCatching { NotificationManagerCompat.from(this).notify(NOTIF_CLIP, n) }
    }

    private fun openAppIntent(): PendingIntent =
        PendingIntent.getActivity(
            this,
            0,
            Intent(this, V2Activity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE,
        )

    private fun serviceAction(action: String): PendingIntent =
        PendingIntent.getService(
            this,
            action.hashCode(),
            Intent(this, ReceiverService::class.java).setAction(action),
            PendingIntent.FLAG_IMMUTABLE,
        )
}
