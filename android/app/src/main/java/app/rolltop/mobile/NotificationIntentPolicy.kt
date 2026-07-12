package app.rolltop.mobile

internal object NotificationIntentPolicy {
    const val EXTRA_TARGET_PATH = "app.rolltop.mobile.extra.TARGET_PATH"
    const val EXTRA_EXPECTED_USER_ID = "app.rolltop.mobile.extra.USER_ID"
    const val EXTRA_EXPECTED_SERVER_URL = "app.rolltop.mobile.extra.SERVER_URL"

    const val NEW_MAIL_NOTIFICATION_ID = 1001
    const val SNOOZE_REMINDER_NOTIFICATION_ID = 1002

    fun contextMatches(
        expectedServer: String,
        expectedUserID: Long,
        currentServer: String,
        currentUserID: Long
    ): Boolean = NotificationActionPolicy.serverMatches(expectedServer, currentServer) &&
        NotificationActionPolicy.userMatches(expectedUserID, currentUserID)
}
