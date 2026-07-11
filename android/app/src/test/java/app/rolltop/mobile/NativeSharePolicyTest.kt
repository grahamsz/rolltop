package app.rolltop.mobile

import org.junit.Assert.assertFalse
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
}
