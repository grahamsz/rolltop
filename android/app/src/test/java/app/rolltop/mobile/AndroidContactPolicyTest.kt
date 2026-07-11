package app.rolltop.mobile

import org.junit.Assert.assertEquals
import org.junit.Test

class AndroidContactPolicyTest {
    @Test
    fun requiresAUsefulBoundedQuery() {
        assertEquals("", AndroidContactPolicy.normalizedQuery(" a "))
        assertEquals("alice", AndroidContactPolicy.normalizedQuery("  alice  "))
        assertEquals(100, AndroidContactPolicy.normalizedQuery("a".repeat(120)).length)
    }

    @Test
    fun suggestionsAreValidatedDeduplicatedAndCapped() {
        val rows = listOf(
            AndroidContactSuggestion(" Alice ", " alice@example.test "),
            AndroidContactSuggestion("Duplicate", "ALICE@example.test"),
            AndroidContactSuggestion("Invalid", "not-an-address")
        ) + (1..12).map { AndroidContactSuggestion("Person $it", "person$it@example.test") }

        val result = AndroidContactPolicy.normalizedSuggestions(rows)

        assertEquals(AndroidContactPolicy.MAX_RESULTS, result.size)
        assertEquals(AndroidContactSuggestion("Alice", "alice@example.test"), result.first())
        assertEquals(1, result.count { it.email.equals("alice@example.test", ignoreCase = true) })
    }
}
