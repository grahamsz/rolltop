package app.rolltop.mobile

internal class UpdatePromptPolicy(initialVersionCode: Int = 0) {
    var lastPromptedVersionCode: Int = initialVersionCode.coerceAtLeast(0)
        private set

    fun shouldPrompt(update: UpdateChecker.ReadyUpdate): Boolean {
        if (update.versionCode <= lastPromptedVersionCode) return false
        lastPromptedVersionCode = update.versionCode
        return true
    }
}
