package app.rolltop.mobile

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class NotificationActionPolicyTest {
    @Test
    fun messageIdsArePositiveUniqueAndBounded() {
        val raw = longArrayOf(-1, 0, 4, 4, 8) + LongArray(110) { (it + 10).toLong() }
        val ids = NotificationActionPolicy.validMessageIDs(raw)
        assertArrayEquals(longArrayOf(4, 8), ids.take(2).toLongArray())
        assertTrue(ids.size == 100)
    }

    @Test
    fun retriesOnlyTransientFailures() {
        assertTrue(NotificationActionPolicy.shouldRetry(0))
        assertTrue(NotificationActionPolicy.shouldRetry(429))
        assertTrue(NotificationActionPolicy.shouldRetry(503))
        assertFalse(NotificationActionPolicy.shouldRetry(401))
        assertFalse(NotificationActionPolicy.shouldRetry(422))
        assertFalse(NotificationActionPolicy.shouldRetry(204))
    }

    @Test
    fun actionRequiresTheNotificationUserToStillBeSignedIn() {
        assertTrue(NotificationActionPolicy.userMatches(7, 7))
        assertFalse(NotificationActionPolicy.userMatches(7, 8))
        assertFalse(NotificationActionPolicy.userMatches(0, 7))
        assertFalse(NotificationActionPolicy.userMatches(7, 0))
    }

    @Test
    fun actionRequiresTheNotificationServerToStillBeConfigured() {
        assertTrue(NotificationActionPolicy.serverMatches("https://MAIL.example:443", "https://mail.example"))
        assertFalse(NotificationActionPolicy.serverMatches("https://mail.example", "https://other.example"))
        assertFalse(NotificationActionPolicy.serverMatches("", "https://mail.example"))
    }

    @Test
    fun notificationTargetsRequireTheOriginalServerAndSignedInUser() {
        assertTrue(
            NotificationIntentPolicy.contextMatches(
                "https://MAIL.example:443",
                7,
                "https://mail.example",
                7
            )
        )
        assertFalse(
            NotificationIntentPolicy.contextMatches(
                "https://mail.example",
                7,
                "https://other.example",
                7
            )
        )
        assertFalse(
            NotificationIntentPolicy.contextMatches(
                "https://mail.example",
                7,
                "https://mail.example",
                8
            )
        )
    }

    @Test
    fun markReadIsOnlyOfferedWhenEveryNotifiedMessageIsPresent() {
        assertTrue(NotificationActionPolicy.includesEveryNotifiedMessage(1, 1))
        assertTrue(NotificationActionPolicy.includesEveryNotifiedMessage(5, 5))
        assertFalse(NotificationActionPolicy.includesEveryNotifiedMessage(23, 5))
        assertFalse(NotificationActionPolicy.includesEveryNotifiedMessage(0, 0))
    }
}
