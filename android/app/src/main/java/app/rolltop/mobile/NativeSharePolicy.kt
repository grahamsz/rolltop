package app.rolltop.mobile

internal object NativeSharePolicy {
    fun acceptsScheme(scheme: String?): Boolean = scheme.equals("content", ignoreCase = true)

    fun shareUrl(serverOrigin: String, sessionID: String, token: String): String =
        serverOrigin.trimEnd('/') + SHARE_PATH + sessionID + "/" + token

    fun requestParts(serverOrigin: String, requestUrl: String): List<String>? {
        val location = RolltopPrefs.internalLocation(serverOrigin, requestUrl) ?: return null
        val path = location.substringBefore('?').substringBefore('#')
        if (!path.startsWith(SHARE_PATH)) return null
        return path.removePrefix(SHARE_PATH).split('/').takeIf { parts ->
            parts.size == 2 && parts.all { it.isNotBlank() }
        }
    }

    private const val SHARE_PATH = "/rolltop-native-share/"
}
