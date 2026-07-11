package app.rolltop.mobile

import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.provider.ContactsContract
import android.webkit.WebView
import androidx.webkit.JavaScriptReplyProxy
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import org.json.JSONObject

class AndroidWebBridge(
    private val activity: Activity,
    private val webView: WebView,
    private val serverOrigin: String,
    private val shareStore: NativeShareStore
) {
    private var pendingContactRequest: PendingContactRequest? = null

    fun attach(): Boolean {
        if (!WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) return false
        WebViewCompat.addWebMessageListener(
            webView,
            BRIDGE_NAME,
            setOf(serverOrigin),
            WebViewCompat.WebMessageListener { _, message, sourceOrigin, isMainFrame, reply ->
                if (!isMainFrame || sourceOrigin.toString().trimEnd('/') != serverOrigin.trimEnd('/')) return@WebMessageListener
                handleMessage(message.data.orEmpty(), reply)
            }
        )
        return true
    }

    fun handleActivityResult(requestCode: Int, resultCode: Int, data: Intent?): Boolean {
        if (requestCode != CONTACT_PICK_REQUEST) return false
        val pending = pendingContactRequest ?: return true
        pendingContactRequest = null
        if (resultCode != Activity.RESULT_OK || data?.data == null) {
            pending.reply.postMessage(response(pending.requestID, true, JSONObject.NULL).toString())
            return true
        }
        val contact = readContactEmail(data.data!!)
        pending.reply.postMessage(response(pending.requestID, true, contact ?: JSONObject.NULL).toString())
        return true
    }

    private fun handleMessage(raw: String, reply: JavaScriptReplyProxy) {
        val request = try {
            JSONObject(raw)
        } catch (_: Exception) {
            reply.postMessage(response("", false, error = "Invalid native request.").toString())
            return
        }
        val requestID = request.optString("requestId", "")
        when (request.optString("action", "")) {
            "sharedFiles" -> {
                val manifest = shareStore.manifest(request.optString("shareId", ""))
                reply.postMessage(response(requestID, manifest != null, manifest, "Shared files are no longer available.").toString())
            }
            "releaseShare" -> {
                shareStore.release(request.optString("shareId", ""))
                reply.postMessage(response(requestID, true, JSONObject.NULL).toString())
            }
            "pickContactEmail" -> activity.runOnUiThread { launchContactPicker(requestID, reply) }
            else -> reply.postMessage(response(requestID, false, error = "Unsupported native action.").toString())
        }
    }

    private fun launchContactPicker(requestID: String, reply: JavaScriptReplyProxy) {
        pendingContactRequest?.let {
            it.reply.postMessage(response(it.requestID, false, error = "Contact picker was replaced by a new request.").toString())
        }
        val intent = Intent(Intent.ACTION_PICK, ContactsContract.CommonDataKinds.Email.CONTENT_URI)
        pendingContactRequest = PendingContactRequest(requestID, reply)
        try {
            activity.startActivityForResult(intent, CONTACT_PICK_REQUEST)
        } catch (_: ActivityNotFoundException) {
            pendingContactRequest = null
            reply.postMessage(response(requestID, false, error = "No Android contacts app is available.").toString())
        }
    }

    private fun readContactEmail(uri: Uri): JSONObject? {
        return try {
            activity.contentResolver.query(
                uri,
                arrayOf(
                    ContactsContract.CommonDataKinds.Email.DISPLAY_NAME,
                    ContactsContract.CommonDataKinds.Email.ADDRESS
                ),
                null,
                null,
                null
            )?.use { cursor ->
                if (!cursor.moveToFirst()) return null
                val nameIndex = cursor.getColumnIndex(ContactsContract.CommonDataKinds.Email.DISPLAY_NAME)
                val emailIndex = cursor.getColumnIndex(ContactsContract.CommonDataKinds.Email.ADDRESS)
                val email = if (emailIndex >= 0) cursor.getString(emailIndex).orEmpty().trim() else ""
                if (email.isBlank()) return null
                JSONObject().apply {
                    put("name", if (nameIndex >= 0) cursor.getString(nameIndex).orEmpty().trim() else "")
                    put("email", email)
                }
            }
        } catch (_: Exception) {
            null
        }
    }

    private fun response(
        requestID: String,
        ok: Boolean,
        result: Any? = null,
        error: String = ""
    ) = JSONObject().apply {
        put("requestId", requestID)
        put("ok", ok)
        if (ok) put("result", result ?: JSONObject.NULL) else put("error", error)
    }

    private data class PendingContactRequest(
        val requestID: String,
        val reply: JavaScriptReplyProxy
    )

    companion object {
        private const val BRIDGE_NAME = "RolltopAndroid"
        private const val CONTACT_PICK_REQUEST = 7101
    }
}
