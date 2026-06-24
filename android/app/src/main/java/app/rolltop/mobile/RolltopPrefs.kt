package app.rolltop.mobile

import android.content.Context
import android.net.Uri

object RolltopPrefs {
    private const val NAME = "rolltop"
    private const val KEY_SERVER_URL = "server_url"
    private const val KEY_LAST_UNREAD = "last_unread"
    private const val KEY_LAST_UPDATE_CHECK = "last_update_check"

    fun serverUrl(context: Context): String =
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE).getString(KEY_SERVER_URL, "")?.trim().orEmpty()

    fun setServerUrl(context: Context, value: String) {
        val normalized = normalizeBaseUrl(value)
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE).edit().putString(KEY_SERVER_URL, normalized).apply()
    }

    fun lastUnread(context: Context): Int =
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE).getInt(KEY_LAST_UNREAD, 0)

    fun setLastUnread(context: Context, value: Int) {
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE).edit().putInt(KEY_LAST_UNREAD, value).apply()
    }

    fun lastUpdateCheck(context: Context): Long =
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE).getLong(KEY_LAST_UPDATE_CHECK, 0)

    fun setLastUpdateCheck(context: Context, value: Long) {
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE).edit().putLong(KEY_LAST_UPDATE_CHECK, value).apply()
    }

    fun buildUrl(context: Context, path: String): String {
        val base = serverUrl(context)
        if (base.isEmpty()) return path
        return base.trimEnd('/') + "/" + path.trimStart('/')
    }

    fun normalizeBaseUrl(value: String): String {
        var raw = value.trim()
        if (raw.isEmpty()) return ""
        if (!raw.startsWith("http://") && !raw.startsWith("https://")) raw = "https://$raw"
        val uri = Uri.parse(raw)
        val scheme = uri.scheme ?: "https"
        val host = uri.authority ?: return raw.trimEnd('/')
        return "$scheme://$host".trimEnd('/')
    }
}
