package app.rolltop.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class RolltopPrefsTest {
    @Test
    fun normalizeBaseUrlAddsHttpsAndCanonicalizesTheOrigin() {
        assertEquals("https://mail.example.com", RolltopPrefs.normalizeBaseUrl(" mail.EXAMPLE.com/ "))
        assertEquals("https://mail.example.com", RolltopPrefs.normalizeBaseUrl("https://mail.example.com:443"))
        assertEquals("https://mail.example.com:8443", RolltopPrefs.normalizeBaseUrl("HTTPS://MAIL.EXAMPLE.COM:8443"))
        assertEquals("https://[2001:db8::1]:8443", RolltopPrefs.normalizeBaseUrl("https://[2001:db8::1]:8443"))
    }

    @Test
    fun normalizeBaseUrlRejectsUnsafeOrNonRootUrls() {
        listOf(
            "http://mail.example.com",
            "ftp://mail.example.com",
            "https://user:password@mail.example.com",
            "https://mail.example.com/rolltop",
            "https://mail.example.com?redirect=elsewhere",
            "https://mail.example.com/#fragment",
            "https://bad host.example.com",
            "not a host"
        ).forEach { value ->
            assertEquals("Expected '$value' to be rejected", "", RolltopPrefs.normalizeBaseUrl(value))
        }
    }

    @Test
    fun internalLocationKeepsOnlySameOriginRoutes() {
        val base = "https://mail.example.com"

        assertEquals(
            "/messages/42?back=%2Fmail#message",
            RolltopPrefs.internalLocation(base, "https://mail.example.com/messages/42?back=%2Fmail#message")
        )
        assertEquals("/mail", RolltopPrefs.internalLocation(base, "https://mail.example.com:443/mail"))
        assertNull(RolltopPrefs.internalLocation(base, "https://other.example.com/mail"))
        assertNull(RolltopPrefs.internalLocation(base, "https://mail.example.com:444/mail"))
        assertNull(RolltopPrefs.internalLocation(base, "javascript:alert(1)"))
    }

    @Test
    fun resolveInternalUrlRejectsProtocolRelativeAndMalformedPaths() {
        val base = "https://mail.example.com"

        assertEquals(
            "https://mail.example.com/search/q/invoices?p=2#result",
            RolltopPrefs.resolveInternalUrl(base, "/search/q/invoices?p=2#result")
        )
        assertNull(RolltopPrefs.resolveInternalUrl(base, "//other.example.com/mail"))
        assertNull(RolltopPrefs.resolveInternalUrl(base, "https://other.example.com/mail"))
        assertNull(RolltopPrefs.resolveInternalUrl(base, "/mail?bad=%"))
    }
}
