package com.example.openair.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.example.openair.FileAttachment
import com.example.openair.MockFiles
import com.example.openair.ui.theme.CaveatFamily
import com.example.openair.ui.theme.InkBlack
import com.example.openair.ui.theme.InkFaded
import com.example.openair.ui.theme.MarkerRed
import com.example.openair.ui.theme.OceanLight
import com.example.openair.ui.theme.OpenAirTheme
import com.example.openair.ui.theme.PaperDeep

/** Map common MIME types to a single representative emoji. */
private fun mimeToEmoji(mimeType: String): String = when {
    mimeType.startsWith("image/")       -> "🖼"
    mimeType.startsWith("video/")       -> "🎬"
    mimeType.startsWith("audio/")       -> "🎵"
    mimeType == "application/pdf"       -> "📄"
    mimeType.startsWith("text/")        -> "📝"
    mimeType.contains("zip")
        || mimeType.contains("archive") -> "🗜"
    else                                 -> "📎"
}

/**
 * Removable file chip shown in the "Attach & Send" area.
 *
 * Renders: [emoji] [name truncated] [size] [×]
 *
 * @param file         the attachment to display
 * @param onRemove     called when user taps the × icon
 */
@Composable
fun FileChip(
    file     : FileAttachment,
    onRemove : (FileAttachment) -> Unit,
    modifier : Modifier = Modifier
) {
    val shape = RoundedCornerShape(6.dp)

    Row(
        modifier = modifier
            .inkOffsetShadow(offsetX = 2.dp, offsetY = 2.dp, cornerRadius = 6.dp)
            .background(color = OceanLight, shape = shape)
            .sketchBorder(strokeWidth = 2.dp, color = InkBlack, cornerRadius = 6.dp)
            .padding(horizontal = 10.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        // Mime icon
        Text(
            text  = mimeToEmoji(file.mimeType),
            style = TextStyle(fontSize = 14.sp)
        )
        Spacer(Modifier.width(6.dp))

        // File name (truncated)
        Text(
            text     = file.name,
            style    = TextStyle(
                fontFamily = CaveatFamily,
                fontWeight = FontWeight.Bold,
                fontSize   = 14.sp,
                color      = InkBlack
            ),
            maxLines = 1,
            overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f, fill = false)
        )
        Spacer(Modifier.width(4.dp))

        // Size label
        Text(
            text  = file.sizeLabel,
            style = TextStyle(fontFamily = CaveatFamily, fontSize = 12.sp, color = InkFaded)
        )
        Spacer(Modifier.width(8.dp))

        // Remove button
        Icon(
            imageVector        = Icons.Default.Close,
            contentDescription = "Remove ${file.name}",
            tint               = MarkerRed,
            modifier           = Modifier
                .clickable { onRemove(file) }
                .padding(2.dp)
        )
    }
}

@Preview(showBackground = true, backgroundColor = 0xFFF5F0E8)
@Composable
private fun FileChipPreview() {
    OpenAirTheme {
        androidx.compose.foundation.layout.Column(
            modifier = Modifier.padding(16.dp),
            verticalArrangement = androidx.compose.foundation.layout.Arrangement.spacedBy(8.dp)
        ) {
            MockFiles.forEach { f -> FileChip(file = f, onRemove = {}) }
        }
    }
}
