package app.rolltop.mobile

import org.junit.Assert.assertEquals
import org.junit.Test

class LoadingRevealGateTest {
    @Test
    fun revealsAfterAnimationThenContent() {
        var reveals = 0
        val gate = LoadingRevealGate { reveals += 1 }

        gate.markAnimationReady()
        assertEquals(0, reveals)

        gate.markContentReady()
        assertEquals(1, reveals)
    }

    @Test
    fun revealsAfterContentThenAnimation() {
        var reveals = 0
        val gate = LoadingRevealGate { reveals += 1 }

        gate.markContentReady()
        assertEquals(0, reveals)

        gate.markAnimationReady()
        assertEquals(1, reveals)
    }

    @Test
    fun duplicateSignalsRevealOnlyOnce() {
        var reveals = 0
        val gate = LoadingRevealGate { reveals += 1 }

        gate.markAnimationReady()
        gate.markAnimationReady()
        gate.markContentReady()
        gate.markContentReady()
        gate.markAnimationReady()

        assertEquals(1, reveals)
    }

    @Test
    fun pendingContentInvalidatesAnEarlierReadySignal() {
        var reveals = 0
        val gate = LoadingRevealGate { reveals += 1 }

        gate.markContentReady()
        gate.markContentPending()
        gate.markAnimationReady()
        assertEquals(0, reveals)

        gate.markContentReady()
        assertEquals(1, reveals)
    }

    @Test
    fun pendingContentDoesNotRearmAnAlreadyRevealedGate() {
        var reveals = 0
        val gate = LoadingRevealGate { reveals += 1 }

        gate.markAnimationReady()
        gate.markContentReady()
        gate.markContentPending()
        gate.markContentReady()

        assertEquals(1, reveals)
    }

    @Test
    fun cancellationPreventsReveal() {
        var reveals = 0
        val gate = LoadingRevealGate { reveals += 1 }

        gate.markAnimationReady()
        gate.cancel()
        gate.markContentReady()

        assertEquals(0, reveals)
    }
}
