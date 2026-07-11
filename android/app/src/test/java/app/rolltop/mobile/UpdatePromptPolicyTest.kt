package app.rolltop.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdatePromptPolicyTest {
    @Test
    fun promptsOnceForEachNewReadyVersion() {
        val policy = UpdatePromptPolicy()

        assertTrue(policy.shouldPrompt(UpdateChecker.ReadyUpdate(100_092, "0.2.92")))
        assertFalse(policy.shouldPrompt(UpdateChecker.ReadyUpdate(100_092, "0.2.92")))
        assertFalse(policy.shouldPrompt(UpdateChecker.ReadyUpdate(100_091, "0.2.91")))
        assertTrue(policy.shouldPrompt(UpdateChecker.ReadyUpdate(100_093, "0.2.93")))
        assertEquals(100_093, policy.lastPromptedVersionCode)
    }

    @Test
    fun restoredActivityDoesNotRepeatTheSameDialog() {
        val policy = UpdatePromptPolicy(100_092)

        assertFalse(policy.shouldPrompt(UpdateChecker.ReadyUpdate(100_092, "0.2.92")))
        assertTrue(policy.shouldPrompt(UpdateChecker.ReadyUpdate(100_093, "0.2.93")))
    }
}
