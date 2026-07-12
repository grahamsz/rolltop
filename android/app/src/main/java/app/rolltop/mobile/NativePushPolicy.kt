package app.rolltop.mobile

import java.net.URI
import java.nio.charset.StandardCharsets
import java.security.MessageDigest

internal data class NativePushSubscription(
    val instance: String,
    val endpoint: String,
    val p256dh: String,
    val auth: String
)

internal enum class PushUploadResult {
    SUCCESS,
    AUTH_REQUIRED,
    RETRY,
    REJECTED
}

internal object NativePushPolicy {
    fun instanceForServerUser(serverUrl: String, userId: Long): String {
        val normalized = RolltopPrefs.normalizeBaseUrl(serverUrl)
        if (normalized.isBlank() || userId <= 0) return ""
        val digest = MessageDigest.getInstance("SHA-256")
            .digest("$normalized\u0000$userId".toByteArray(StandardCharsets.UTF_8))
        return "rolltop-" + digest.take(16).joinToString("") { byte -> "%02x".format(byte.toInt() and 0xff) }
    }

    fun validVAPIDKey(value: String): Boolean =
        value.length == 87 && value.all { it in 'a'..'z' || it in 'A'..'Z' || it in '0'..'9' || it == '-' || it == '_' }

    fun subscription(
        expectedInstance: String,
        instance: String,
        endpoint: String,
        p256dh: String,
        auth: String
    ): NativePushSubscription? {
        if (expectedInstance.isBlank() || instance != expectedInstance) return null
        if (endpoint.length !in 1..2_048 || p256dh.length !in 1..512 || auth.length !in 1..512) return null
        val uri = try {
            URI(endpoint)
        } catch (_: Exception) {
            return null
        }
        if (uri.isOpaque || !uri.scheme.equals("https", ignoreCase = true) || uri.host.isNullOrBlank()) return null
        if (uri.rawUserInfo != null || uri.rawFragment != null) return null
        if (uri.port > 65_535) return null
        if (p256dh.isBlank() || auth.isBlank()) return null
        return NativePushSubscription(instance, uri.toASCIIString(), p256dh.trim(), auth.trim())
    }

    fun acceptsMessage(expectedInstance: String, instance: String, decrypted: Boolean): Boolean =
        expectedInstance.isNotBlank() && instance == expectedInstance && decrypted

    fun uploadResult(statusCode: Int?): PushUploadResult = when {
        statusCode == null -> PushUploadResult.RETRY
        statusCode in 200..299 -> PushUploadResult.SUCCESS
        statusCode == 401 || statusCode == 403 -> PushUploadResult.AUTH_REQUIRED
        statusCode == 408 || statusCode == 425 || statusCode == 429 || statusCode >= 500 -> PushUploadResult.RETRY
        else -> PushUploadResult.REJECTED
    }

    fun shouldRetryRegistration(reason: String): Boolean = reason == "NETWORK" || reason == "INTERNAL_ERROR"

    fun registrationRetryDelay(attempt: Int): Long? = when (attempt) {
        0 -> 30_000L
        1 -> 5 * 60_000L
        2 -> 30 * 60_000L
        else -> null
    }
}
