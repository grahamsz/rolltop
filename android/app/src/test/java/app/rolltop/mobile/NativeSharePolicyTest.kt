package app.rolltop.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class NativeSharePolicyTest {
    @Test
    fun onlyAndroidContentProviderStreamsAreAccepted() {
        assertTrue(NativeSharePolicy.acceptsScheme("content"))
        assertTrue(NativeSharePolicy.acceptsScheme("CONTENT"))
        assertFalse(NativeSharePolicy.acceptsScheme("file"))
        assertFalse(NativeSharePolicy.acceptsScheme("https"))
        assertFalse(NativeSharePolicy.acceptsScheme(null))
    }

    @Test
    fun sharedStreamsUseAReservedSameOriginPath() {
        assertEquals(
            "https://mail.example.test/rolltop-native-share/session/token",
            NativeSharePolicy.shareUrl("https://mail.example.test/", "session", "token")
        )
        assertEquals(
            listOf("session", "token"),
            NativeSharePolicy.requestParts(
                "https://mail.example.test",
                "https://mail.example.test/rolltop-native-share/session/token"
            )
        )
    }

    @Test
    fun sharedStreamRequestsRejectOtherOriginsAndMalformedPaths() {
        val origin = "https://mail.example.test"
        assertNull(
            NativeSharePolicy.requestParts(
                origin,
                "https://other.example.test/rolltop-native-share/session/token"
            )
        )
        assertNull(NativeSharePolicy.requestParts(origin, "$origin/rolltop-native-share/session"))
        assertNull(NativeSharePolicy.requestParts(origin, "$origin/rolltop-native-share/session/token/extra"))
        assertNull(NativeSharePolicy.requestParts(origin, "$origin/mail"))
        assertNull(NativeSharePolicy.requestParts(origin, "$origin/rolltop-native-share/session/token", "POST"))
    }
}
