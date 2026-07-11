package app.rolltop.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class AndroidBackNavigationTest {
    @Test
    fun directMessageReturnsToItsEncodedFolder() {
        assertEquals(
            "https://mail.example.test/mailbox/12/p2?view=compact",
            AndroidBackNavigation.messageFallbackUrl(
                "https://mail.example.test",
                "https://mail.example.test/messages/42?back=%2Fmailbox%2F12%2Fp2%3Fview%3Dcompact"
            )
        )
    }

    @Test
    fun directMessageWithoutASafeBackTargetReturnsToAllMail() {
        val origin = "https://mail.example.test"
        assertEquals(
            "$origin/mail",
            AndroidBackNavigation.messageFallbackUrl(origin, "$origin/messages/42")
        )
        assertEquals(
            "$origin/mail",
            AndroidBackNavigation.messageFallbackUrl(
                origin,
                "$origin/messages/42?back=https%3A%2F%2Fother.example.test%2Fmail"
            )
        )
    }

    @Test
    fun nonMessageAndForeignPagesDoNotOverrideNormalActivityBack() {
        val origin = "https://mail.example.test"
        assertNull(AndroidBackNavigation.messageFallbackUrl(origin, "$origin/mailbox/12"))
        assertNull(
            AndroidBackNavigation.messageFallbackUrl(
                origin,
                "https://other.example.test/messages/42?back=%2Fmail"
            )
        )
        assertNull(AndroidBackNavigation.messageFallbackUrl(origin, null))
    }
}
