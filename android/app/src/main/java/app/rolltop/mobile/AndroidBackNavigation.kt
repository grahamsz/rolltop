package app.rolltop.mobile

import java.net.URI
import java.net.URLDecoder
import java.nio.charset.StandardCharsets

internal object AndroidBackNavigation {
    fun messageFallbackUrl(serverOrigin: String, currentUrl: String?): String? {
        if (currentUrl.isNullOrBlank()) return null
        val location = RolltopPrefs.internalLocation(serverOrigin, currentUrl) ?: return null
        val uri = try {
            URI(location)
        } catch (_: Exception) {
            return null
        }
        if (!uri.path.orEmpty().startsWith("/messages/")) return null

        val requestedBack = queryValue(uri.rawQuery, "back")
        return RolltopPrefs.resolveInternalUrl(serverOrigin, requestedBack.orEmpty())
            ?: RolltopPrefs.resolveInternalUrl(serverOrigin, "/mail")
    }

    private fun queryValue(rawQuery: String?, name: String): String? {
        for (part in rawQuery.orEmpty().split('&')) {
            if (part.isBlank()) continue
            val pair = part.split('=', limit = 2)
            val key = decode(pair[0]) ?: continue
            if (key != name) continue
            return decode(pair.getOrElse(1) { "" })
        }
        return null
    }

    private fun decode(value: String): String? = try {
        URLDecoder.decode(value, StandardCharsets.UTF_8.name())
    } catch (_: IllegalArgumentException) {
        null
    }
}
