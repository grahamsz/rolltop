package app.rolltop.mobile

import org.junit.Assert.assertEquals
import org.junit.Test

class WebNavigationPolicyTest {
    private val origin = "https://mail.example.test"
    private val clicked = WebNavigationContext(isMainFrame = true, hasUserGesture = true)

    @Test
    fun sameOriginNavigationStaysInRolltop() {
        assertEquals(
            WebNavigationAction.ALLOW_IN_WEBVIEW,
            WebNavigationPolicy.action(origin, "$origin/messages/42?back=%2Fmail", clicked)
        )
        assertEquals(
            WebNavigationAction.ALLOW_IN_WEBVIEW,
            WebNavigationPolicy.action(origin, "https://mail.example.test:443/mail", clicked)
        )
        assertEquals(
            WebNavigationAction.OPEN_INTERNAL,
            WebNavigationPolicy.action(origin, "$origin/contacts", clicked.copy(isNewWindow = true))
        )
    }

    @Test
    fun clickedExternalWebLinksOpenOutsideRolltop() {
        listOf(
            "https://www.example.test/article#section",
            "http://legacy.example.test/info",
            "https://mail.example.test:8443/mail"
        ).forEach { candidate ->
            assertEquals(
                "Expected $candidate to open externally",
                WebNavigationAction.OPEN_EXTERNAL,
                WebNavigationPolicy.action(origin, candidate, clicked)
            )
        }
    }

    @Test
    fun externalRedirectsAndBackgroundNavigationsCannotLaunchApps() {
        val external = "https://outside.example.test/landing"
        listOf(
            clicked.copy(isRedirect = true),
            clicked.copy(hasUserGesture = false),
            clicked.copy(isMainFrame = false)
        ).forEach { context ->
            assertEquals(
                WebNavigationAction.BLOCK,
                WebNavigationPolicy.action(origin, external, context)
            )
        }
    }

    @Test
    fun clickedMailtoLinksComposeInRolltop() {
        assertEquals(
            WebNavigationAction.COMPOSE_MAILTO,
            WebNavigationPolicy.action(origin, "MAILTO:person@example.test?subject=Hello", clicked)
        )
        assertEquals(
            WebNavigationAction.BLOCK,
            WebNavigationPolicy.action(
                origin,
                "mailto:person@example.test",
                clicked.copy(hasUserGesture = false)
            )
        )
    }

    @Test
    fun unsafeOrMalformedSchemesAreBlocked() {
        listOf(
            "javascript:alert(1)",
            "intent://scan/#Intent;scheme=zxing;end",
            "file:///sdcard/private.txt",
            "content://app.rolltop.mobile/private",
            "data:text/html,hello",
            "https://user:password@example.test/",
            "https:///missing-host",
            "not a url"
        ).forEach { candidate ->
            assertEquals(
                "Expected $candidate to be blocked",
                WebNavigationAction.BLOCK,
                WebNavigationPolicy.action(origin, candidate, clicked)
            )
        }
    }
}
