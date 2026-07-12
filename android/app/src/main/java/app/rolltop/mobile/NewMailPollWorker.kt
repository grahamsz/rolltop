package app.rolltop.mobile

import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequest
import androidx.work.OutOfQuotaPolicy
import androidx.work.PeriodicWorkRequest
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import org.json.JSONObject
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.util.concurrent.TimeUnit

internal data class NewMailPollResult(
    val userId: Long = 0,
    val authenticated: Boolean = false,
    val shouldRetry: Boolean = false
)

class NewMailPollWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    override fun doWork(): Result {
        val result = synchronized(POLL_LOCK) {
            NewMailPoller.poll(applicationContext)
        }
        return if (result.shouldRetry && runAttemptCount < MAX_RETRY_ATTEMPTS) Result.retry() else Result.success()
    }

    companion object {
        const val EXTRA_TARGET_PATH = "app.rolltop.mobile.extra.TARGET_PATH"
        private const val UNIQUE_WORK_NAME = "rolltop-new-mail-poll"
        private const val UNIQUE_IMMEDIATE_WORK_NAME = "rolltop-new-mail-immediate"
        private const val MAX_RETRY_ATTEMPTS = 3
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

        fun enqueueImmediate(context: Context) {
            val constraints = Constraints.Builder()
                .setRequiredNetworkType(NetworkType.CONNECTED)
                .build()
            val request = OneTimeWorkRequest.Builder(NewMailPollWorker::class.java)
                .setConstraints(constraints)
                .setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
                .build()
            WorkManager.getInstance(context).enqueueUniqueWork(
                UNIQUE_IMMEDIATE_WORK_NAME,
                ExistingWorkPolicy.APPEND_OR_REPLACE,
                request
            )
        }

        internal fun prepareForPushRegistration(context: Context): NewMailPollResult =
            synchronized(POLL_LOCK) { NewMailPoller.poll(context) }

    }
}

private object NewMailPoller {
    private const val NOTIFICATION_ID = 1001
    private const val NOTIFICATION_REQUEST_CODE = 100
    private const val MAX_NOTIFICATION_LINES = 5
    private const val MAX_TEXT_LENGTH = 120
    private const val PREWARM_CONNECT_TIMEOUT_MILLIS = 1_500
    private const val PREWARM_READ_TIMEOUT_MILLIS = 2_500

    fun poll(context: Context): NewMailPollResult {
        val base = RolltopPrefs.serverUrl(context)
        if (base.isBlank()) return NewMailPollResult()
        val previous = RolltopPrefs.newMailCursor(context)
        val path = if (previous == null) {
            "/api/notifications/new-mail"
        } else {
            "/api/notifications/new-mail?after=${previous.eventId}"
        }
        val response = HttpJson.getResponse(context, "$base$path")
            ?: return NewMailPollResult(shouldRetry = true)
        if (response.statusCode == 401 || response.statusCode == 403) return NewMailPollResult()
        if (NativePushPolicy.uploadResult(response.statusCode) == PushUploadResult.RETRY) {
            return NewMailPollResult(shouldRetry = true)
        }
        if (response.statusCode !in 200..299) return NewMailPollResult()
        val json = response.body ?: return NewMailPollResult(shouldRetry = true)
        val userId = json.optLong("user_id", 0)
        val cursor = json.optLong("cursor", -1)
        val count = json.optInt("count", 0)
        val decision = NewMailNotificationPolicy.decide(previous, userId, cursor, count)
            ?: return NewMailPollResult()
        if (!decision.shouldNotify) {
            return if (RolltopPrefs.setNewMailCursor(context, decision.cursor.userId, decision.cursor.eventId)) {
                NewMailPollResult(userId, authenticated = true)
            } else {
                NewMailPollResult(shouldRetry = true)
            }
        }

        val messages = newMailMessages(json)
        prewarm(context, count, messages)
        // Persist immediately before posting so a restarted worker cannot replay the alert.
        if (!RolltopPrefs.setNewMailCursor(context, decision.cursor.userId, decision.cursor.eventId)) {
            return NewMailPollResult(shouldRetry = true)
        }
        showNotification(context, count, messages)
        return NewMailPollResult(userId, authenticated = true)
    }

    private fun prewarm(context: Context, count: Int, messages: List<NewMailMessage>) {
        val single = count == 1 && messages.size == 1
        val path = if (single) "/api/messages/${messages[0].messageId}/prefetch" else "/api/mail?page=1"
        HttpJson.get(
            context,
            RolltopPrefs.buildUrl(context, path),
            connectTimeoutMillis = PREWARM_CONNECT_TIMEOUT_MILLIS,
            readTimeoutMillis = PREWARM_READ_TIMEOUT_MILLIS
        )
    }

    private fun newMailMessages(json: JSONObject): List<NewMailMessage> {
        val raw = json.optJSONArray("messages") ?: return emptyList()
        val messages = ArrayList<NewMailMessage>(raw.length())
        for (index in 0 until raw.length()) {
            val item = raw.optJSONObject(index) ?: continue
            val eventId = item.optLong("event_id", 0)
            val messageId = item.optLong("message_id", 0)
            if (eventId <= 0 || messageId <= 0) continue
            messages.add(NewMailMessage(eventId, messageId, item.optString("from_addr"), item.optString("subject")))
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
                .putExtra(NewMailPollWorker.EXTRA_TARGET_PATH, targetPath)
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
            builder.setContentTitle(sender).setContentText(subject)
                .setStyle(NotificationCompat.BigTextStyle().bigText(subject))
        } else {
            val shown = messages.takeLast(MAX_NOTIFICATION_LINES)
            val style = NotificationCompat.InboxStyle().setBigContentTitle("$count new messages")
            for (message in shown) {
                style.addLine("${notificationSender(message.from)}: ${notificationSubject(message.subject)}")
            }
            if (count > shown.size) style.setSummaryText("${count - shown.size} more")
            builder.setContentTitle("$count new messages")
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
}
