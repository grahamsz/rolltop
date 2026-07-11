package app.rolltop.mobile

internal data class NewMailCursor(val userId: Long, val eventId: Long)

internal data class NewMailCursorDecision(
    val cursor: NewMailCursor,
    val shouldNotify: Boolean
)

internal object NewMailNotificationPolicy {
    fun decide(previous: NewMailCursor?, userId: Long, eventId: Long, count: Int): NewMailCursorDecision? {
        if (userId <= 0 || eventId < 0 || count < 0) return null
        val next = NewMailCursor(userId, eventId)
        if (previous == null || previous.userId != userId || eventId < previous.eventId) {
            return NewMailCursorDecision(next, shouldNotify = false)
        }
        return NewMailCursorDecision(
            cursor = next,
            shouldNotify = count > 0 && eventId > previous.eventId
        )
    }
}
