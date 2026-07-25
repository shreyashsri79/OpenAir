package com.example.openair.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.lifecycle.viewmodel.compose.viewModel
import com.example.openair.OpenAirCallbacks
import com.example.openair.OpenAirViewModel

/**
 * Stateful wrapper for [OpenAirContent].
 *
 * Responsibilities:
 * 1. Instantiates [OpenAirViewModel] via [viewModel()]
 * 2. Collects [OpenAirViewModel.uiState] as Compose State
 * 3. Constructs the [OpenAirCallbacks] object wiring UI events → ViewModel
 * 4. Passes state + callbacks into the pure [OpenAirContent] composable
 *
 * No rendering logic lives here — this is strictly glue code.
 */
@Composable
fun OpenAirScreen(
    modifier: Modifier = Modifier,
    vm      : OpenAirViewModel = viewModel()
) {
    val state by vm.uiState.collectAsState()

    // ── File picker ───────────────────────────────────────────────────────────
    // GetMultipleContents lets the user pick one or more files of any type.
    // The result callback runs on the main thread; we hand each Uri to the
    // ViewModel which resolves metadata on Dispatchers.IO.
    val filePicker = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.GetMultipleContents()
    ) { uris ->
        uris.forEach { vm.onFilePicked(it) }
    }

    // Folder picker — the tree is zipped in the ViewModel, then attached.
    val folderPicker = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.OpenDocumentTree()
    ) { uri ->
        uri?.let { vm.onFolderPicked(it) }
    }

    val callbacks = OpenAirCallbacks(
        onToggleReceiver   = vm::toggleReceiver,
        onScanClick        = vm::startScan,
        onDeviceConnect    = vm::connectDevice,
        onDeviceDisconnect = vm::disconnectDevice,
        onAttachFile       = { filePicker.launch("*/*") },
        onAttachFolder     = { folderPicker.launch(null) },
        onRemoveFile       = vm::removeFile,
        onPasteTextChange  = vm::updatePastedText,
        onSendClick        = vm::send,
        onDismissError     = vm::dismissError,
        onAcceptTransfer   = vm::acceptTransfer,
        onRejectTransfer   = vm::rejectTransfer,
        onOpenReceivedFile = vm::openReceivedFile,
        onClearReceived    = vm::clearReceivedHistory,
    )

    OpenAirContent(
        state     = state,
        callbacks = callbacks,
        modifier  = modifier
    )
}
