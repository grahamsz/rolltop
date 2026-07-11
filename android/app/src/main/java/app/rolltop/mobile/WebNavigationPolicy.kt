package app.rolltop.mobile

import java.net.URI

internal enum class WebNavigationAction {
    ALLOW_IN_WEBVIEW,
    OPEN_INTERNAL,
    COMPOSE_MAILTO,
    OPEN_EXTERNAL,
    BLOCK
}

internal data class WebNavigationContext(
    val isMainFrame: Boolean,
    val hasUserGesture: Boolean,
    val isRedirect: Boolean = false,
    val isNewWindow: Boolean = false
)

internal object WebNavigationPolicy {
    fun action(
        serverOrigin: String,
        candidate: String,
        context: WebNavigationContext
    ): WebNavigationAction {
        val uri = parse(candidate) ?: return WebNavigationAction.BLOCK
        return when (uri.scheme?.lowercase()) {
            "http", "https" -> webAction(serverOrigin, candidate, uri, context)
            "mailto" -> if (context.isMainFrame && context.hasUserGesture && !context.isRedirect) {
                WebNavigationAction.COMPOSE_MAILTO
            } else {
                WebNavigationAction.BLOCK
            }
            else -> WebNavigationAction.BLOCK
        }
    }

    private fun webAction(
        serverOrigin: String,
        candidate: String,
        uri: URI,
        context: WebNavigationContext
    ): WebNavigationAction {
        if (uri.host.isNullOrBlank() || uri.rawUserInfo != null) return WebNavigationAction.BLOCK
        if (RolltopPrefs.internalLocation(serverOrigin, candidate) != null) {
            return if (context.isNewWindow) {
                WebNavigationAction.OPEN_INTERNAL
            } else {
                WebNavigationAction.ALLOW_IN_WEBVIEW
            }
        }
        return if (context.isMainFrame && context.hasUserGesture && !context.isRedirect) {
            WebNavigationAction.OPEN_EXTERNAL
        } else {
            WebNavigationAction.BLOCK
        }
    }

    private fun parse(candidate: String): URI? = try {
        URI(candidate.trim()).takeIf { it.isAbsolute }
    } catch (_: Exception) {
        null
    }
}
