package app.rolltop.mobile

import android.content.Context
import android.content.Intent
import android.database.Cursor
import android.net.Uri
import android.provider.OpenableColumns
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import androidx.core.content.IntentCompat
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.util.UUID

class NativeShareStore(private val context: Context) {
    fun capture(intent: Intent): String? {
        if (intent.action != Intent.ACTION_SEND && intent.action != Intent.ACTION_SEND_MULTIPLE) return null
        val uris = linkedSetOf<Uri>()
        intent.clipData?.let { clip ->
            for (index in 0 until clip.itemCount) clip.getItemAt(index).uri?.let(uris::add)
        }
        IntentCompat.getParcelableExtra(intent, Intent.EXTRA_STREAM, Uri::class.java)?.let(uris::add)
        IntentCompat.getParcelableArrayListExtra(intent, Intent.EXTRA_STREAM, Uri::class.java)?.let(uris::addAll)
        if (uris.isEmpty()) return null

        val items = uris.mapNotNull { uri -> itemFor(uri, intent.type) }
        if (items.isEmpty()) return null
        val sessionID = UUID.randomUUID().toString()
        synchronized(sessions) {
            pruneLocked()
            sessions[sessionID] = ShareSession(System.currentTimeMillis(), items)
        }
        return sessionID
    }

    fun manifest(sessionID: String, serverOrigin: String): JSONObject? {
        val session = synchronized(sessions) {
            pruneLocked()
            sessions[sessionID]
        } ?: return null
        return JSONObject().apply {
            put("shareId", sessionID)
            put("files", JSONArray().apply {
                session.items.forEach { item ->
                    put(JSONObject().apply {
                        put("name", item.name)
                        put("type", item.mimeType)
                        put("size", item.size)
                        put("url", NativeSharePolicy.shareUrl(serverOrigin, sessionID, item.token))
                    })
                }
            })
        }
    }

    fun release(sessionID: String) {
        synchronized(sessions) { sessions.remove(sessionID) }
    }

    fun intercept(request: WebResourceRequest, allowedOrigin: String): WebResourceResponse? {
        val parts = NativeSharePolicy.requestParts(allowedOrigin, request.url.toString(), request.method) ?: return null
        val item = synchronized(sessions) { sessions[parts[0]]?.items?.find { it.token == parts[1] } }
            ?: return notFound(allowedOrigin)
        val input = try {
            context.contentResolver.openInputStream(item.uri)
        } catch (_: Exception) {
            null
        } ?: return notFound(allowedOrigin)
        return WebResourceResponse(
            item.mimeType,
            null,
            200,
            "OK",
            responseHeaders(allowedOrigin),
            input
        )
    }

    private fun itemFor(uri: Uri, fallbackMimeType: String?): ShareItem? {
        if (!NativeSharePolicy.acceptsScheme(uri.scheme)) return null
        if (uri.authority == "${BuildConfig.APPLICATION_ID}.files") return null
        var displayName = "attachment"
        var size = -1L
        try {
            context.contentResolver.query(
                uri,
                arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE),
                null,
                null,
                null
            )?.use { cursor ->
                if (cursor.moveToFirst()) {
                    displayName = cursor.stringValue(OpenableColumns.DISPLAY_NAME).orEmpty().trim().ifBlank { "attachment" }
                    size = cursor.longValue(OpenableColumns.SIZE) ?: -1L
                }
            }
        } catch (_: Exception) {
            // Some providers expose a stream but not metadata.
        }
        val resolvedMimeType = try {
            context.contentResolver.getType(uri)
        } catch (_: Exception) {
            null
        }
        val mimeType = resolvedMimeType
            ?.substringBefore(';')
            ?.trim()
            ?.takeIf { it.contains('/') }
            ?: fallbackMimeType?.substringBefore(';')?.trim()?.takeIf { it.contains('/') }
            ?: "application/octet-stream"
        return ShareItem(UUID.randomUUID().toString(), uri, displayName, mimeType, size)
    }

    private fun pruneLocked() {
        val cutoff = System.currentTimeMillis() - SESSION_TTL_MS
        sessions.entries.removeAll { it.value.createdAt < cutoff }
    }

    private fun notFound(allowedOrigin: String) = WebResourceResponse(
        "text/plain",
        "UTF-8",
        404,
        "Not Found",
        responseHeaders(allowedOrigin),
        ByteArrayInputStream("Not found".toByteArray())
    )

    private fun responseHeaders(allowedOrigin: String) = mapOf(
        "Access-Control-Allow-Origin" to allowedOrigin,
        "Cache-Control" to "no-store",
        "Vary" to "*",
        "X-Content-Type-Options" to "nosniff"
    )

    private fun Cursor.stringValue(column: String): String? {
        val index = getColumnIndex(column)
        return if (index >= 0 && !isNull(index)) getString(index) else null
    }

    private fun Cursor.longValue(column: String): Long? {
        val index = getColumnIndex(column)
        return if (index >= 0 && !isNull(index)) getLong(index) else null
    }

    private data class ShareSession(val createdAt: Long, val items: List<ShareItem>)
    private data class ShareItem(
        val token: String,
        val uri: Uri,
        val name: String,
        val mimeType: String,
        val size: Long
    )

    companion object {
        private val sessions = mutableMapOf<String, ShareSession>()
        private const val SESSION_TTL_MS = 15 * 60_000L
    }
}
