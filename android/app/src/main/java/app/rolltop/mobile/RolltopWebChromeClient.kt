package app.rolltop.mobile

import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Intent
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebView
import android.widget.Toast

class RolltopWebChromeClient(private val activity: Activity) : WebChromeClient() {
    private var pendingFiles: ValueCallback<Array<android.net.Uri>>? = null

    override fun onShowFileChooser(
        webView: WebView,
        filePathCallback: ValueCallback<Array<android.net.Uri>>,
        fileChooserParams: FileChooserParams
    ): Boolean {
        pendingFiles?.onReceiveValue(null)
        pendingFiles = filePathCallback
        return try {
            activity.startActivityForResult(fileChooserParams.createIntent(), FILE_CHOOSER_REQUEST)
            true
        } catch (_: ActivityNotFoundException) {
            pendingFiles = null
            filePathCallback.onReceiveValue(null)
            Toast.makeText(activity, "No file picker is available.", Toast.LENGTH_SHORT).show()
            false
        }
    }

    fun handleActivityResult(requestCode: Int, resultCode: Int, data: Intent?): Boolean {
        if (requestCode != FILE_CHOOSER_REQUEST) return false
        pendingFiles?.onReceiveValue(FileChooserParams.parseResult(resultCode, data))
        pendingFiles = null
        return true
    }

    fun cancelPendingRequest() {
        pendingFiles?.onReceiveValue(null)
        pendingFiles = null
    }

    companion object {
        private const val FILE_CHOOSER_REQUEST = 7102
    }
}
