package app.rolltop.mobile

import android.content.Context
import java.net.IDN
import java.net.URI

object RolltopPrefs {
    data class ReadyUpdate(val versionCode: Int, val versionName: String)

    private const val NAME = "rolltop"
    private const val KEY_SERVER_URL = "server_url"
    private const val KEY_LAST_LOCATION = "last_location"
    private const val KEY_NEW_MAIL_USER_ID = "new_mail_user_id"
    private const val KEY_NEW_MAIL_EVENT_ID = "new_mail_event_id"
    private const val KEY_LAST_UPDATE_CHECK = "last_update_check"
    private const val KEY_READY_UPDATE_CODE = "ready_update_code"
    private const val KEY_READY_UPDATE_NAME = "ready_update_name"

    fun serverUrl(context: Context): String {
        val stored = context.getSharedPreferences(NAME, Context.MODE_PRIVATE)
            .getString(KEY_SERVER_URL, "")
            .orEmpty()
        return normalizeBaseUrl(stored)
    }

    fun setServerUrl(context: Context, value: String): Boolean {
        val normalized = normalizeBaseUrl(value)
        if (normalized.isEmpty()) return false

        val preferences = context.getSharedPreferences(NAME, Context.MODE_PRIVATE)
        val previous = normalizeBaseUrl(preferences.getString(KEY_SERVER_URL, "").orEmpty())
        preferences.edit().apply {
            putString(KEY_SERVER_URL, normalized)
            if (previous != normalized) {
                remove(KEY_LAST_LOCATION)
                remove(KEY_NEW_MAIL_USER_ID)
                remove(KEY_NEW_MAIL_EVENT_ID)
                remove(KEY_LAST_UPDATE_CHECK)
                remove(KEY_READY_UPDATE_CODE)
                remove(KEY_READY_UPDATE_NAME)
            }
        }.apply()
        return true
    }

    fun lastVisitedUrl(context: Context): String {
        val path = context.getSharedPreferences(NAME, Context.MODE_PRIVATE)
            .getString(KEY_LAST_LOCATION, "")
            .orEmpty()
        return resolveInternalUrl(serverUrl(context), path).orEmpty()
    }

    fun rememberVisitedUrl(context: Context, value: String?) {
        if (value.isNullOrBlank()) return
        val path = internalLocation(serverUrl(context), value.orEmpty()) ?: return
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE)
            .edit()
            .putString(KEY_LAST_LOCATION, path)
            .apply()
    }

    fun resolveInternalUrl(context: Context, path: String?): String? =
        resolveInternalUrl(serverUrl(context), path.orEmpty())

    internal fun newMailCursor(context: Context): NewMailCursor? {
        val preferences = context.getSharedPreferences(NAME, Context.MODE_PRIVATE)
        val userId = preferences.getLong(KEY_NEW_MAIL_USER_ID, 0)
        if (userId <= 0 || !preferences.contains(KEY_NEW_MAIL_EVENT_ID)) return null
        return NewMailCursor(userId, preferences.getLong(KEY_NEW_MAIL_EVENT_ID, 0).coerceAtLeast(0))
    }

    fun setNewMailCursor(context: Context, userId: Long, eventId: Long): Boolean {
        if (userId <= 0 || eventId < 0) return false
        return context.getSharedPreferences(NAME, Context.MODE_PRIVATE).edit()
            .putLong(KEY_NEW_MAIL_USER_ID, userId)
            .putLong(KEY_NEW_MAIL_EVENT_ID, eventId)
            .commit()
    }

    fun lastUpdateCheck(context: Context): Long =
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE).getLong(KEY_LAST_UPDATE_CHECK, 0)

    fun setLastUpdateCheck(context: Context, value: Long) {
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE).edit().putLong(KEY_LAST_UPDATE_CHECK, value).apply()
    }

    fun readyUpdate(context: Context): ReadyUpdate? {
        val preferences = context.getSharedPreferences(NAME, Context.MODE_PRIVATE)
        val code = preferences.getInt(KEY_READY_UPDATE_CODE, 0)
        val name = preferences.getString(KEY_READY_UPDATE_NAME, "").orEmpty()
        return if (code > 0 && name.isNotBlank()) ReadyUpdate(code, name) else null
    }

    fun setReadyUpdate(context: Context, versionCode: Int, versionName: String) {
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE).edit()
            .putInt(KEY_READY_UPDATE_CODE, versionCode)
            .putString(KEY_READY_UPDATE_NAME, versionName)
            .apply()
    }

    fun clearReadyUpdate(context: Context) {
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE).edit()
            .remove(KEY_READY_UPDATE_CODE)
            .remove(KEY_READY_UPDATE_NAME)
            .apply()
    }

    fun buildUrl(context: Context, path: String): String {
        val base = serverUrl(context)
        if (base.isEmpty()) return path
        return base.trimEnd('/') + "/" + path.trimStart('/')
    }

    fun normalizeBaseUrl(value: String): String {
        var raw = value.trim()
        if (raw.isEmpty()) return ""
        if (!raw.contains("://")) raw = "https://$raw"

        val uri = try {
            URI(raw)
        } catch (_: Exception) {
            return ""
        }
        if (uri.isOpaque || !uri.scheme.equals("https", ignoreCase = true)) return ""
        if (uri.rawUserInfo != null || uri.rawQuery != null || uri.rawFragment != null) return ""
        if (uri.rawPath.orEmpty() !in setOf("", "/")) return ""

        val host = uri.host?.takeIf { it.isNotBlank() } ?: return ""
        val port = uri.port
        if (port != -1 && port !in 1..65535) return ""
        val normalizedHost = try {
            if (host.contains(':')) host.lowercase() else IDN.toASCII(host).lowercase()
        } catch (_: IllegalArgumentException) {
            return ""
        }

        return try {
            URI("https", null, normalizedHost, if (port == 443) -1 else port, null, null, null).toASCIIString()
        } catch (_: Exception) {
            ""
        }
    }

    internal fun internalLocation(baseUrl: String, candidate: String): String? {
        val base = parseBaseUrl(baseUrl) ?: return null
        val resolved = try {
            base.resolve(candidate)
        } catch (_: Exception) {
            return null
        }
        if (!sameOrigin(base, resolved)) return null
        val path = resolved.rawPath?.takeIf { it.startsWith('/') } ?: "/"
        return buildString {
            append(path)
            resolved.rawQuery?.let { append('?').append(it) }
            resolved.rawFragment?.let { append('#').append(it) }
        }
    }

    internal fun resolveInternalUrl(baseUrl: String, path: String): String? {
        if (!path.startsWith('/') || path.startsWith("//")) return null
        val base = parseBaseUrl(baseUrl) ?: return null
        val resolved = try {
            base.resolve(path)
        } catch (_: Exception) {
            return null
        }
        return if (sameOrigin(base, resolved)) resolved.toASCIIString() else null
    }

    private fun parseBaseUrl(value: String): URI? {
        val normalized = normalizeBaseUrl(value)
        if (normalized.isEmpty()) return null
        return try {
            URI(normalized.trimEnd('/') + "/")
        } catch (_: Exception) {
            null
        }
    }

    private fun sameOrigin(left: URI, right: URI): Boolean =
        left.scheme.equals(right.scheme, ignoreCase = true) &&
            left.host.equals(right.host, ignoreCase = true) &&
            effectivePort(left) == effectivePort(right)

    private fun effectivePort(uri: URI): Int = if (uri.port >= 0) uri.port else 443
}
