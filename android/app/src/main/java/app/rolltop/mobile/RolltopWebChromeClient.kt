package app.rolltop.mobile

import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Intent
import android.graphics.Bitmap
import android.os.Message
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Toast

class RolltopWebChromeClient(
    private val activity: Activity,
    private val onPopupNavigation: (PopupNavigation) -> Unit
) : WebChromeClient() {
    private var pendingFiles: ValueCallback<Array<android.net.Uri>>? = null
    private val popupWebViews = mutableSetOf<WebView>()

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

    override fun onCreateWindow(
        view: WebView,
        isDialog: Boolean,
        isUserGesture: Boolean,
        resultMsg: Message
    ): Boolean {
        if (!isUserGesture) return false
        val transport = resultMsg.obj as? WebView.WebViewTransport ?: return false
        val popup = WebView(activity)
        var handled = false

        fun dispatch(url: String, isRedirect: Boolean) {
            if (handled || url == ABOUT_BLANK) return
            handled = true
            popup.stopLoading()
            onPopupNavigation(PopupNavigation(url, isUserGesture, isRedirect))
            popup.post { destroyPopup(popup) }
        }

        popup.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                if (request.url.toString() == ABOUT_BLANK) return false
                dispatch(request.url.toString(), request.isRedirect)
                return true
            }

            override fun onPageStarted(view: WebView, url: String, favicon: Bitmap?) {
                dispatch(url, false)
            }
        }
        popupWebViews += popup
        transport.webView = popup
        resultMsg.sendToTarget()
        return true
    }

    override fun onCloseWindow(window: WebView) {
        destroyPopup(window)
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
        popupWebViews.toList().forEach(::destroyPopup)
    }

    private fun destroyPopup(popup: WebView) {
        if (!popupWebViews.remove(popup)) return
        popup.stopLoading()
        popup.destroy()
    }

    data class PopupNavigation(
        val url: String,
        val hasUserGesture: Boolean,
        val isRedirect: Boolean
    )

    companion object {
        private const val FILE_CHOOSER_REQUEST = 7102
        private const val ABOUT_BLANK = "about:blank"
    }
}
