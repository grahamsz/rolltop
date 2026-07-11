package app.rolltop.mobile

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdateAPKValidatorTest {
    @Test
    fun sameSignerAndValidRotationLineageAreAccepted() {
        assertTrue(compatible(setOf("old"), setOf("old"), setOf("old")))
        assertTrue(compatible(setOf("old"), setOf("new"), setOf("old", "new")))
    }

    @Test
    fun unrelatedOrReversedSignerLineageIsRejected() {
        assertFalse(compatible(setOf("old"), setOf("other"), setOf("other")))
        assertFalse(compatible(setOf("new"), setOf("old"), setOf("old")))
        assertFalse(compatible(emptySet(), setOf("new"), setOf("new")))
    }

    @Test
    fun MultipleSignerUpdatesRequireTheExactSignerSet() {
        assertTrue(compatible(setOf("a", "b"), setOf("a", "b"), setOf("a", "b"), true, true))
        assertFalse(compatible(setOf("a", "b"), setOf("a"), setOf("a"), true, false))
        assertFalse(compatible(setOf("a", "b"), setOf("a", "c"), setOf("a", "c"), true, true))
    }

    private fun compatible(
        installed: Set<String>,
        candidate: Set<String>,
        history: Set<String>,
        installedMultiple: Boolean = false,
        candidateMultiple: Boolean = false
    ): Boolean = UpdateAPKValidator.signingLineageCompatible(
        installed,
        candidate,
        history,
        installedMultiple,
        candidateMultiple
    )
}
