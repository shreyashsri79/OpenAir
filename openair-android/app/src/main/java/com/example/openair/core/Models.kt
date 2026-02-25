package com.example.openair.core

import android.net.Uri

data class Device(
    val name: String,
    val host: String,
    val port: Int
)

data class PickedFile(
    val uri: Uri,
    val name: String,
    val size: Long
)

sealed class SendItem {
    data class UriFile(val file: PickedFile) : SendItem()
    data class TextFile(val name: String, val bytes: ByteArray) : SendItem()
}
