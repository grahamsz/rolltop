package app.rolltop.mobile

import android.app.Activity
import android.Manifest
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.provider.ContactsContract
import android.webkit.WebView
import androidx.webkit.JavaScriptReplyProxy
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.RejectedExecutionException

class AndroidWebBridge(
    private val activity: Activity,
    private val webView: WebView,
    private val serverOrigin: String,
    private val shareStore: NativeShareStore,
    private val requestContactPermission: () -> Unit
) {
    private var pendingContactRequest: PendingContactRequest? = null
    private var pendingContactAccessRequest: PendingContactRequest? = null
    private var contactPermissionRequestInFlight = false
    private val contactExecutor = Executors.newSingleThreadExecutor()
    private val pushExecutor = Executors.newSingleThreadExecutor()
    @Volatile
    private var closed = false

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

    fun handleContactPermissionResult(granted: Boolean) {
        contactPermissionRequestInFlight = false
        val pending = pendingContactAccessRequest ?: return
        pendingContactAccessRequest = null
        val access = if (granted) {
            CONTACT_ACCESS_GRANTED
        } else {
            CONTACT_ACCESS_DENIED
        }
        pending.reply.postMessage(response(pending.requestID, true, contactResult(access)).toString())
    }

    fun close() {
        if (closed) return
        closed = true
        contactExecutor.shutdownNow()
        pushExecutor.shutdownNow()
        pendingContactRequest = null
        pendingContactAccessRequest = null
    }

    private fun handleMessage(raw: String, reply: JavaScriptReplyProxy) {
        if (closed) return
        val request = try {
            JSONObject(raw)
        } catch (_: Exception) {
            reply.postMessage(response("", false, error = "Invalid native request.").toString())
            return
        }
        val requestID = request.optString("requestId", "")
        when (request.optString("action", "")) {
            "sharedFiles" -> {
                val manifest = shareStore.manifest(request.optString("shareId", ""), serverOrigin)
                reply.postMessage(response(requestID, manifest != null, manifest, "Shared files are no longer available.").toString())
            }
            "releaseShare" -> {
                shareStore.release(request.optString("shareId", ""))
                reply.postMessage(response(requestID, true, JSONObject.NULL).toString())
            }
            "pickContactEmail" -> activity.runOnUiThread { launchContactPicker(requestID, reply) }
            "contactSuggestions" -> {
                val query = AndroidContactPolicy.normalizedQuery(request.optString("query", ""))
                activity.runOnUiThread { loadContactSuggestions(requestID, query, reply) }
            }
            "requestContactAccess" -> activity.runOnUiThread { requestContactAccess(requestID, reply) }
            "pushSubscription" -> loadPushSubscription(requestID, reply)
            "registerPush" -> {
                activity.runOnUiThread { NativePushRegistration.maybeRegister(activity) }
                reply.postMessage(response(requestID, true, JSONObject.NULL).toString())
            }
            "unregisterPush" -> {
                NativePushRegistration.unregister(activity)
                reply.postMessage(response(requestID, true, JSONObject.NULL).toString())
            }
            else -> reply.postMessage(response(requestID, false, error = "Unsupported native action.").toString())
        }
    }

    private fun loadPushSubscription(requestID: String, reply: JavaScriptReplyProxy) {
        try {
            pushExecutor.execute {
                if (closed) return@execute
                val bootstrap = HttpJson.get(activity, RolltopPrefs.buildUrl(activity, "/api/bootstrap"))
                val userId = bootstrap?.optJSONObject("user")?.optLong("id", 0) ?: 0
                activity.runOnUiThread {
                    if (!closed && !activity.isFinishing && !activity.isDestroyed) {
                        reply.postMessage(response(requestID, true, pushSubscriptionResult(userId)).toString())
                    }
                }
            }
        } catch (_: RejectedExecutionException) {
            reply.postMessage(response(requestID, true, JSONObject.NULL).toString())
        }
    }

    private fun pushSubscriptionResult(authenticatedUserId: Long): Any {
        val subscription = NativePushStore.subscription(activity) ?: return JSONObject.NULL
        if (authenticatedUserId <= 0 || NativePushStore.ownerUserId(activity, subscription.instance) != authenticatedUserId) {
            return JSONObject.NULL
        }
        val expected = NativePushPolicy.instanceForServerUser(RolltopPrefs.serverUrl(activity), authenticatedUserId)
        if (subscription.instance != expected) return JSONObject.NULL
        return JSONObject().apply {
            put("endpoint", subscription.endpoint)
            put("keys", JSONObject().apply {
                put("p256dh", subscription.p256dh)
                put("auth", subscription.auth)
            })
        }
    }

    private fun requestContactAccess(requestID: String, reply: JavaScriptReplyProxy) {
        if (closed) return
        if (hasContactAccess()) {
            reply.postMessage(response(requestID, true, contactResult(CONTACT_ACCESS_GRANTED)).toString())
            return
        }
        pendingContactAccessRequest?.let {
            it.reply.postMessage(response(it.requestID, true, contactResult(CONTACT_ACCESS_DENIED)).toString())
        }
        pendingContactAccessRequest = PendingContactRequest(requestID, reply)
        if (contactPermissionRequestInFlight) return
        contactPermissionRequestInFlight = true
        requestContactPermission()
    }

    private fun loadContactSuggestions(requestID: String, query: String, reply: JavaScriptReplyProxy) {
        if (closed) return
        if (!hasContactAccess()) {
            reply.postMessage(response(requestID, true, contactResult(CONTACT_ACCESS_REQUIRED)).toString())
            return
        }
        if (query.isBlank()) {
            reply.postMessage(response(requestID, true, contactResult(CONTACT_ACCESS_GRANTED)).toString())
            return
        }
        try {
            contactExecutor.execute {
                if (closed) return@execute
                val contacts = readContactSuggestions(query)
                activity.runOnUiThread {
                    if (closed || activity.isFinishing || activity.isDestroyed) return@runOnUiThread
                    val access = if (contacts == null) CONTACT_ACCESS_REQUIRED else CONTACT_ACCESS_GRANTED
                    reply.postMessage(response(requestID, true, contactResult(access, contacts.orEmpty())).toString())
                }
            }
        } catch (_: RejectedExecutionException) {
            // The WebView was replaced while this request was moving to the worker.
        }
    }

    private fun hasContactAccess(): Boolean =
        activity.checkSelfPermission(Manifest.permission.READ_CONTACTS) == PackageManager.PERMISSION_GRANTED

    private fun readContactSuggestions(query: String): List<AndroidContactSuggestion>? {
        return try {
            val filterURI = Uri.withAppendedPath(
                ContactsContract.CommonDataKinds.Email.CONTENT_FILTER_URI,
                Uri.encode(query)
            ).buildUpon()
                .appendQueryParameter(ContactsContract.LIMIT_PARAM_KEY, (AndroidContactPolicy.MAX_RESULTS * 4).toString())
                .build()
            val rows = mutableListOf<AndroidContactSuggestion>()
            activity.contentResolver.query(
                filterURI,
                arrayOf(
                    ContactsContract.CommonDataKinds.Email.DISPLAY_NAME_PRIMARY,
                    ContactsContract.CommonDataKinds.Email.ADDRESS
                ),
                null,
                null,
                "${ContactsContract.CommonDataKinds.Email.IS_PRIMARY} DESC, " +
                    "${ContactsContract.CommonDataKinds.Email.DISPLAY_NAME_PRIMARY} COLLATE NOCASE"
            )?.use { cursor ->
                val nameIndex = cursor.getColumnIndex(ContactsContract.CommonDataKinds.Email.DISPLAY_NAME_PRIMARY)
                val emailIndex = cursor.getColumnIndex(ContactsContract.CommonDataKinds.Email.ADDRESS)
                while (cursor.moveToNext() && rows.size < AndroidContactPolicy.MAX_RESULTS * 4) {
                    rows += AndroidContactSuggestion(
                        if (nameIndex >= 0) cursor.getString(nameIndex).orEmpty() else "",
                        if (emailIndex >= 0) cursor.getString(emailIndex).orEmpty() else ""
                    )
                }
            }
            AndroidContactPolicy.normalizedSuggestions(rows)
        } catch (_: SecurityException) {
            null
        } catch (_: Exception) {
            emptyList()
        }
    }

    private fun contactResult(
        access: String,
        contacts: List<AndroidContactSuggestion> = emptyList()
    ) = JSONObject().apply {
        put("access", access)
        put("contacts", JSONArray().apply {
            contacts.forEach { contact ->
                put(JSONObject().apply {
                    put("name", contact.name)
                    put("email", contact.email)
                })
            }
        })
    }

    private fun launchContactPicker(requestID: String, reply: JavaScriptReplyProxy) {
        if (closed) return
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
        private const val CONTACT_ACCESS_GRANTED = "granted"
        private const val CONTACT_ACCESS_REQUIRED = "permission_required"
        private const val CONTACT_ACCESS_DENIED = "denied"
    }
}
