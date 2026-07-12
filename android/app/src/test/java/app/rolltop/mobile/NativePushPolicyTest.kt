package app.rolltop.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class NativePushPolicyTest {
    @Test
    fun instanceIsStableAndBoundToServerAndUser() {
        val first = NativePushPolicy.instanceForServerUser("https://MAIL.example.com:443", 7)

        assertTrue(first.startsWith("rolltop-"))
        assertEquals(first, NativePushPolicy.instanceForServerUser("https://mail.example.com", 7))
        assertNotEquals(first, NativePushPolicy.instanceForServerUser("https://mail.example.com", 8))
        assertNotEquals(first, NativePushPolicy.instanceForServerUser("https://other.example.com", 7))
        assertEquals("", NativePushPolicy.instanceForServerUser("https://mail.example.com", 0))
    }

    @Test
    fun vapidKeyRequiresTheWebPushUncompressedEncodingShape() {
        assertTrue(NativePushPolicy.validVAPIDKey("B" + "a".repeat(86)))
        assertFalse(NativePushPolicy.validVAPIDKey("a".repeat(86)))
        assertFalse(NativePushPolicy.validVAPIDKey("B" + "+".repeat(86)))
    }

    @Test
    fun subscriptionIsHttpsAndInstanceBound() {
        val saved = NativePushPolicy.subscription(
            expectedInstance = "rolltop-current",
            instance = "rolltop-current",
            endpoint = "https://fcm.googleapis.com/wp/example?token=1",
            p256dh = "public-key",
            auth = "auth-secret"
        )

        assertNotNull(saved)
        assertEquals("https://fcm.googleapis.com/wp/example?token=1", saved?.endpoint)
        assertNull(NativePushPolicy.subscription("rolltop-current", "rolltop-old", saved!!.endpoint, "key", "auth"))
        assertNull(NativePushPolicy.subscription("rolltop-current", "rolltop-current", "http://push.example.test/a", "key", "auth"))
        assertNull(NativePushPolicy.subscription("rolltop-current", "rolltop-current", "https://user@push.example.test/a", "key", "auth"))
        assertNull(NativePushPolicy.subscription("rolltop-current", "rolltop-current", "https://push.example.test/a#fragment", "key", "auth"))
        assertNull(NativePushPolicy.subscription("rolltop-current", "rolltop-current", "https://push.example.test/a", "", "auth"))
    }

    @Test
    fun onlyDecryptedMessagesForTheCurrentInstanceWakePolling() {
        assertTrue(NativePushPolicy.acceptsMessage("rolltop-current", "rolltop-current", decrypted = true))
        assertFalse(NativePushPolicy.acceptsMessage("rolltop-current", "rolltop-old", decrypted = true))
        assertFalse(NativePushPolicy.acceptsMessage("rolltop-current", "rolltop-current", decrypted = false))
    }

    @Test
    fun uploadFailuresRetryOnlyWhenTheyCanRecoverWithoutLogin() {
        assertEquals(PushUploadResult.SUCCESS, NativePushPolicy.uploadResult(204))
        assertEquals(PushUploadResult.AUTH_REQUIRED, NativePushPolicy.uploadResult(401))
        assertEquals(PushUploadResult.AUTH_REQUIRED, NativePushPolicy.uploadResult(403))
        assertEquals(PushUploadResult.RETRY, NativePushPolicy.uploadResult(null))
        assertEquals(PushUploadResult.RETRY, NativePushPolicy.uploadResult(429))
        assertEquals(PushUploadResult.RETRY, NativePushPolicy.uploadResult(503))
        assertEquals(PushUploadResult.REJECTED, NativePushPolicy.uploadResult(400))
    }

    @Test
    fun distributorRetriesOnlyTransientRegistrationFailures() {
        assertTrue(NativePushPolicy.shouldRetryRegistration("NETWORK"))
        assertTrue(NativePushPolicy.shouldRetryRegistration("INTERNAL_ERROR"))
        assertFalse(NativePushPolicy.shouldRetryRegistration("ACTION_REQUIRED"))
        assertFalse(NativePushPolicy.shouldRetryRegistration("VAPID_REQUIRED"))
        assertEquals(30_000L, NativePushPolicy.registrationRetryDelay(0))
        assertEquals(300_000L, NativePushPolicy.registrationRetryDelay(1))
        assertEquals(1_800_000L, NativePushPolicy.registrationRetryDelay(2))
        assertNull(NativePushPolicy.registrationRetryDelay(3))
    }
}
