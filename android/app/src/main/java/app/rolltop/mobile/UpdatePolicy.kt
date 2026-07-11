package app.rolltop.mobile

import org.json.JSONObject
import java.net.URI

data class UpdateOffer(
    val versionCode: Int,
    val versionName: String,
    val apkURL: String,
    val sha256: String
)

object UpdatePolicy {
    private val sha256Pattern = Regex("^[a-fA-F0-9]{64}$")

    fun shouldOffer(candidateVersionCode: Int, installedVersionCode: Int): Boolean =
        candidateVersionCode > installedVersionCode

    fun needsAPKDownload(candidateVersionCode: Int, cachedVersionCode: Int?): Boolean =
        candidateVersionCode != cachedVersionCode

    fun validSHA256(value: String): Boolean = value.isBlank() || sha256Pattern.matches(value)

    fun parseOffer(serverBaseURL: String, installedVersionCode: Int, metadata: JSONObject): UpdateOffer? {
        val versionCode = metadata.optInt("versionCode", 0)
        if (!shouldOffer(versionCode, installedVersionCode)) return null
        val versionName = metadata.optString("versionName", "new version").trim().ifBlank { "new version" }
        val sha256 = metadata.optString("sha256", "").trim()
        if (!validSHA256(sha256)) return null
        val apkURL = resolveAPKURL(serverBaseURL, metadata.optString("apkUrl", "")) ?: return null
        return UpdateOffer(versionCode, versionName, apkURL, sha256)
    }

    fun resolveAPKURL(serverBaseURL: String, candidateURL: String): String? {
        if (candidateURL.isBlank()) return null
        return try {
            val base = URI(serverBaseURL.trimEnd('/') + "/")
            val resolved = base.resolve(candidateURL.trim())
            if (base.scheme != "https" || resolved.scheme != "https") return null
            if (!sameOrigin(base, resolved) || resolved.userInfo != null || resolved.fragment != null) return null
            resolved.toASCIIString()
        } catch (_: Exception) {
            null
        }
    }

    private fun sameOrigin(left: URI, right: URI): Boolean =
        left.host.equals(right.host, ignoreCase = true) && effectivePort(left) == effectivePort(right)

    private fun effectivePort(uri: URI): Int = when {
        uri.port >= 0 -> uri.port
        uri.scheme.equals("https", ignoreCase = true) -> 443
        else -> -1
    }
}
