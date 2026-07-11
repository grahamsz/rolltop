package app.rolltop.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test

class NewMailNotificationPolicyTest {
    @Test
    fun firstPollSilentlyBaselinesEvenWithHistoricalCount() {
        val decision = NewMailNotificationPolicy.decide(null, userId = 7, eventId = 4_000, count = 4_000)

        assertNotNull(decision)
        assertEquals(NewMailCursor(7, 4_000), decision?.cursor)
        assertFalse(decision?.shouldNotify ?: true)
    }

    @Test
    fun sameUserForwardDeltaNotifies() {
        val decision = NewMailNotificationPolicy.decide(NewMailCursor(7, 100), userId = 7, eventId = 103, count = 3)

        assertNotNull(decision)
        assertTrue(decision?.shouldNotify == true)
    }

    @Test
    fun accountSwitchSilentlyRebindsCursor() {
        val decision = NewMailNotificationPolicy.decide(NewMailCursor(7, 100), userId = 8, eventId = 2, count = 2)

        assertEquals(NewMailCursor(8, 2), decision?.cursor)
        assertFalse(decision?.shouldNotify ?: true)
    }

    @Test
    fun cursorRollbackSilentlyRebaselinesAfterServerRestore() {
        val decision = NewMailNotificationPolicy.decide(NewMailCursor(7, 100), userId = 7, eventId = 4, count = 0)

        assertEquals(NewMailCursor(7, 4), decision?.cursor)
        assertFalse(decision?.shouldNotify ?: true)
    }
}
