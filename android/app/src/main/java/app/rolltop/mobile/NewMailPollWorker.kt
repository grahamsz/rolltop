package app.rolltop.mobile

import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequest
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import org.json.JSONObject
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.util.concurrent.TimeUnit

class NewMailPollWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    override fun doWork(): Result {
        synchronized(POLL_LOCK) {
            poll(applicationContext)
        }
        return Result.success()
    }

    private fun poll(context: Context) {
        val base = RolltopPrefs.serverUrl(context)
        if (base.isBlank()) return
        val previous = RolltopPrefs.newMailCursor(context)
        val path = if (previous == null) {
            "/api/notifications/new-mail"
        } else {
            "/api/notifications/new-mail?after=${previous.eventId}"
        }
        val json = HttpJson.get(context, "$base$path") ?: return
        val userId = json.optLong("user_id", 0)
        val cursor = json.optLong("cursor", -1)
        val count = json.optInt("count", 0)
        val decision = NewMailNotificationPolicy.decide(previous, userId, cursor, count) ?: return
        if (!decision.shouldNotify) {
            RolltopPrefs.setNewMailCursor(context, decision.cursor.userId, decision.cursor.eventId)
            return
        }

        val messages = newMailMessages(json)
        prewarm(context, count, messages)
        // Persist immediately before posting so a restarted worker cannot replay the alert.
        if (!RolltopPrefs.setNewMailCursor(context, decision.cursor.userId, decision.cursor.eventId)) return
        showNotification(context, count, messages)
    }

    private fun prewarm(context: Context, count: Int, messages: List<NewMailMessage>) {
        val single = count == 1 && messages.size == 1
        if (single) {
            HttpJson.get(
                context,
                RolltopPrefs.buildUrl(context, "/api/messages/${messages[0].messageId}/prefetch"),
                connectTimeoutMillis = PREWARM_CONNECT_TIMEOUT_MILLIS,
                readTimeoutMillis = PREWARM_READ_TIMEOUT_MILLIS
            )
        } else {
            HttpJson.get(
                context,
                RolltopPrefs.buildUrl(context, "/api/mail?page=1"),
                connectTimeoutMillis = PREWARM_CONNECT_TIMEOUT_MILLIS,
                readTimeoutMillis = PREWARM_READ_TIMEOUT_MILLIS
            )
        }
    }

    private fun newMailMessages(json: JSONObject): List<NewMailMessage> {
        val raw = json.optJSONArray("messages") ?: return emptyList()
        val messages = ArrayList<NewMailMessage>(raw.length())
        for (index in 0 until raw.length()) {
            val item = raw.optJSONObject(index) ?: continue
            val eventId = item.optLong("event_id", 0)
            val messageId = item.optLong("message_id", 0)
            if (eventId <= 0 || messageId <= 0) continue
            messages.add(
                NewMailMessage(
                    eventId = eventId,
                    messageId = messageId,
                    from = item.optString("from_addr"),
                    subject = item.optString("subject")
                )
            )
        }
        return messages
    }

    private fun showNotification(context: Context, count: Int, messages: List<NewMailMessage>) {
        NotificationChannels.ensure(context)
        val single = count == 1 && messages.size == 1
        val targetPath = if (single) {
            val back = URLEncoder.encode("/mail", StandardCharsets.UTF_8.name())
            "/messages/${messages[0].messageId}?back=$back"
        } else {
            "/mail"
        }
        val openIntent = PendingIntent.getActivity(
            context,
            NOTIFICATION_REQUEST_CODE,
            Intent(context, MainActivity::class.java)
                .putExtra(EXTRA_TARGET_PATH, targetPath)
                .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val builder = NotificationCompat.Builder(context, NotificationChannels.MAIL)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentIntent(openIntent)
            .setAutoCancel(true)
            .setCategory(NotificationCompat.CATEGORY_EMAIL)
            .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
            .setNumber(count)

        if (single) {
            val message = messages[0]
            val sender = notificationSender(message.from)
            val subject = notificationSubject(message.subject)
            builder
                .setContentTitle(sender)
                .setContentText(subject)
                .setStyle(NotificationCompat.BigTextStyle().bigText(subject))
        } else {
            val shown = messages.takeLast(MAX_NOTIFICATION_LINES)
            val style = NotificationCompat.InboxStyle()
                .setBigContentTitle("$count new messages")
            for (message in shown) {
                style.addLine("${notificationSender(message.from)}: ${notificationSubject(message.subject)}")
            }
            if (count > shown.size) style.setSummaryText("${count - shown.size} more")
            builder
                .setContentTitle("$count new messages")
                .setContentText(multipleNotificationSummary(shown))
                .setStyle(style)
        }
        try {
            context.getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, builder.build())
        } catch (_: SecurityException) {
            // Android 13+ can revoke notification permission between polling and posting.
        }
    }

    private fun multipleNotificationSummary(messages: List<NewMailMessage>): String {
        if (messages.isEmpty()) return "Open All Mail"
        return messages.takeLast(2).joinToString(", ") { notificationSender(it.from) }
    }

    private fun notificationSender(raw: String): String {
        val compact = raw.trim().replace(Regex("\\s+"), " ")
        var value = compact.substringBefore('<').trim().trim('"')
        if (value.isBlank()) value = compact.substringAfter('<', compact).substringBefore('>').trim()
        if ('@' in value) value = value.substringBefore('@')
        return value.ifBlank { "New message" }.take(MAX_TEXT_LENGTH)
    }

    private fun notificationSubject(raw: String): String {
        val compact = raw.trim().replace(Regex("\\s+"), " ")
        return compact.ifBlank { "(No subject)" }.take(MAX_TEXT_LENGTH)
    }

    private data class NewMailMessage(
        val eventId: Long,
        val messageId: Long,
        val from: String,
        val subject: String
    )

    companion object {
        const val EXTRA_TARGET_PATH = "app.rolltop.mobile.extra.TARGET_PATH"
        private const val UNIQUE_WORK_NAME = "rolltop-new-mail-poll"
        private const val NOTIFICATION_ID = 1001
        private const val NOTIFICATION_REQUEST_CODE = 100
        private const val MAX_NOTIFICATION_LINES = 5
        private const val MAX_TEXT_LENGTH = 120
        private const val PREWARM_CONNECT_TIMEOUT_MILLIS = 1_500
        private const val PREWARM_READ_TIMEOUT_MILLIS = 2_500
        private val POLL_LOCK = Any()

        fun schedule(context: Context) {
            val constraints = Constraints.Builder()
                .setRequiredNetworkType(NetworkType.CONNECTED)
                .build()
            val request = PeriodicWorkRequest.Builder(
                NewMailPollWorker::class.java,
                15,
                TimeUnit.MINUTES
            )
                .setInitialDelay(1, TimeUnit.MINUTES)
                .setConstraints(constraints)
                .build()
            WorkManager.getInstance(context).enqueueUniquePeriodicWork(
                UNIQUE_WORK_NAME,
                ExistingPeriodicWorkPolicy.UPDATE,
                request
            )
        }
    }
}
