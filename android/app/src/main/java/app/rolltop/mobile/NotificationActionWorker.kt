package app.rolltop.mobile

import android.content.Context
import androidx.work.Worker
import androidx.work.WorkerParameters
import org.json.JSONArray
import org.json.JSONObject

class NotificationActionWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    override fun doWork(): Result {
        val messageIDs = NotificationActionPolicy.validMessageIDs(
            inputData.getLongArray(KEY_MESSAGE_IDS) ?: longArrayOf()
        )
        val expectedUserID = inputData.getLong(KEY_EXPECTED_USER_ID, 0)
        val expectedServer = inputData.getString(KEY_EXPECTED_SERVER_URL).orEmpty()
        val currentServer = RolltopPrefs.serverUrl(applicationContext)
        if (messageIDs.isEmpty() || expectedUserID <= 0 ||
            !NotificationActionPolicy.serverMatches(expectedServer, currentServer)
        ) return Result.success()
        val bootstrap = HttpJson.getResponse(
            applicationContext,
            "$currentServer/api/bootstrap"
        ) ?: return retryOrFinish()
        if (bootstrap.statusCode == 401 || bootstrap.statusCode == 403) return Result.success()
        if (bootstrap.statusCode !in 200..299) {
            return if (NotificationActionPolicy.shouldRetry(bootstrap.statusCode)) retryOrFinish() else Result.success()
        }
        val csrf = bootstrap.body?.optString("csrf").orEmpty()
        val userID = bootstrap.body?.optJSONObject("user")?.optLong("id", 0) ?: 0
        if (csrf.isBlank() || !NotificationActionPolicy.userMatches(expectedUserID, userID)) return Result.success()
        if (!NotificationActionPolicy.serverMatches(expectedServer, RolltopPrefs.serverUrl(applicationContext))) {
            return Result.success()
        }
        val body = JSONObject()
            .put("ids", JSONArray(messageIDs.toList()))
            .put("read", true)
        val response = HttpJson.post(
            applicationContext,
            "$currentServer/api/messages/bulk-read",
            body,
            csrf
        ) ?: return retryOrFinish()
        return if (NotificationActionPolicy.shouldRetry(response.statusCode)) retryOrFinish() else Result.success()
    }

    private fun retryOrFinish(): Result =
        if (runAttemptCount < MAX_RETRY_ATTEMPTS) Result.retry() else Result.success()

    companion object {
        const val KEY_MESSAGE_IDS = "message_ids"
        const val KEY_EXPECTED_USER_ID = "expected_user_id"
        const val KEY_EXPECTED_SERVER_URL = "expected_server_url"
        private const val MAX_RETRY_ATTEMPTS = 3
    }
}

object NotificationActionPolicy {
    private const val MAX_MESSAGE_IDS = 100

    fun validMessageIDs(raw: LongArray): LongArray = raw.asSequence()
        .filter { it > 0 }
        .distinct()
        .take(MAX_MESSAGE_IDS)
        .toList()
        .toLongArray()

    fun shouldRetry(statusCode: Int): Boolean =
        statusCode == 0 || statusCode == 408 || statusCode == 425 || statusCode == 429 || statusCode >= 500

    fun userMatches(expectedUserID: Long, currentUserID: Long): Boolean =
        expectedUserID > 0 && currentUserID > 0 && expectedUserID == currentUserID

    fun serverMatches(expectedServer: String, currentServer: String): Boolean {
        val expected = RolltopPrefs.normalizeBaseUrl(expectedServer)
        val current = RolltopPrefs.normalizeBaseUrl(currentServer)
        return expected.isNotBlank() && expected == current
    }

    fun includesEveryNotifiedMessage(count: Int, envelopeCount: Int): Boolean =
        count > 0 && envelopeCount > 0 && count == envelopeCount
}
