package app.rolltop.mobile

/** Coordinates the independently completing native animation and WebView render. */
internal class LoadingRevealGate(
    private val onReveal: () -> Unit
) {
    private var animationReady = false
    private var contentReady = false
    private var cancelled = false
    private var revealed = false

    fun markAnimationReady() {
        animationReady = true
        maybeReveal()
    }

    fun markContentReady() {
        contentReady = true
        maybeReveal()
    }

    fun markContentPending() {
        if (cancelled || revealed) return
        contentReady = false
    }

    fun cancel() {
        cancelled = true
    }

    private fun maybeReveal() {
        if (cancelled || revealed || !animationReady || !contentReady) return
        revealed = true
        onReveal()
    }
}
