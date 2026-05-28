package com.example.openair.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.wrapContentSize
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.example.openair.ui.theme.CaveatFamily
import com.example.openair.ui.theme.InkBlack
import com.example.openair.ui.theme.OceanDeep
import com.example.openair.ui.theme.PaperWhite

/**
 * A hand-drawn aesthetic button: solid fill rectangle with a hard [inkOffsetShadow]
 * and a 2dp [sketchBorder]. Works as both a full-width CTA and a compact action.
 *
 * @param label           button text
 * @param onClick         click handler
 * @param modifier        layout modifier (pass [Modifier.fillMaxWidth()] for full-width CTA)
 * @param icon            optional leading icon
 * @param backgroundColor fill color (default [OceanDeep])
 * @param contentColor    text and icon tint (default [PaperWhite])
 * @param shadowOffsetX   horizontal shadow distance
 * @param shadowOffsetY   vertical shadow distance
 * @param cornerRadius    rounding of the button corners
 * @param fontSize        label font size
 * @param contentPadding  inner padding
 */
@Composable
fun ActionMarkerButton(
    label          : String,
    onClick        : () -> Unit,
    modifier       : Modifier        = Modifier,
    icon           : ImageVector?    = null,
    backgroundColor: Color           = OceanDeep,
    contentColor   : Color           = PaperWhite,
    shadowOffsetX  : Dp              = 4.dp,
    shadowOffsetY  : Dp              = 4.dp,
    cornerRadius   : Dp              = 8.dp,
    fontSize       : TextUnit        = 18.sp,
    contentPadding : PaddingValues   = PaddingValues(horizontal = 20.dp, vertical = 12.dp)
) {
    val shape = RoundedCornerShape(cornerRadius)

    Box(
        modifier = modifier
            .inkOffsetShadow(
                offsetX      = shadowOffsetX,
                offsetY      = shadowOffsetY,
                shadowColor  = InkBlack,
                cornerRadius = cornerRadius
            )
            .background(color = backgroundColor, shape = shape)
            .sketchBorder(strokeWidth = 2.dp, color = InkBlack, cornerRadius = cornerRadius)
            .clickable(onClick = onClick)
            .padding(contentPadding),
        contentAlignment = Alignment.Center
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.wrapContentSize()
        ) {
            if (icon != null) {
                Icon(
                    imageVector = icon,
                    contentDescription = null,
                    tint = contentColor
                )
                Spacer(Modifier.width(8.dp))
            }
            Text(
                text  = label,
                style = TextStyle(
                    fontFamily = CaveatFamily,
                    fontWeight = FontWeight.Bold,
                    fontSize   = fontSize,
                    color      = contentColor,
                    textAlign  = TextAlign.Center
                )
            )
        }
    }
}
