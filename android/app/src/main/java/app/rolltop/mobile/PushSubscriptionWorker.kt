package app.rolltop.mobile

import android.content.Context
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequest
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import org.json.JSONObject

class PushSubscriptionWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    override fun doWork(): Result {
        val context = applicationContext
        val subscription = NativePushStore.subscription(context) ?: return Result.success()
        val baseline = NewMailPollWorker.prepareForPushRegistration(context)
        if (baseline.shouldRetry) return transientResult()
        if (!baseline.authenticated || baseline.userId <= 0) return Result.success()

        val expected = NativePushStore.expectedInstance(context)
        val owner = NativePushStore.ownerUserId(context, subscription.instance)
        if (subscription.instance != expected) {
            NativePushStore.clearEndpoint(context, subscription.instance)
            return Result.success()
        }
        if (owner > 0 && owner != baseline.userId) {
            NativePushRegistration.unregister(context)
            return Result.success()
        }

        val bootstrap = HttpJson.getResponse(context, RolltopPrefs.buildUrl(context, "/api/bootstrap"))
        if (bootstrap == null) return transientResult()
        if (bootstrap.statusCode == 401 || bootstrap.statusCode == 403) return Result.success()
        if (NativePushPolicy.uploadResult(bootstrap.statusCode) == PushUploadResult.RETRY) return transientResult()
        val bootstrapBody = bootstrap.body ?: return transientResult()
        val bootstrapUser = bootstrapBody.optJSONObject("user")?.optLong("id", 0) ?: 0
        val csrf = bootstrapBody.optString("csrf", "")
        if (bootstrapUser != baseline.userId || csrf.isBlank()) return Result.success()

        val current = NativePushStore.subscription(context)
        if (current != subscription) {
            return Result.success()
        }

        val body = JSONObject().apply {
            put("endpoint", subscription.endpoint)
            put("keys", JSONObject().apply {
                put("p256dh", subscription.p256dh)
                put("auth", subscription.auth)
            })
        }
        val response = HttpJson.post(
            context,
            RolltopPrefs.buildUrl(context, "/api/push/subscription"),
            body,
            csrf
        )
        return when (NativePushPolicy.uploadResult(response?.statusCode)) {
            PushUploadResult.SUCCESS -> {
                if (NativePushStore.markUploaded(context, subscription, baseline.userId)) {
                    NewMailPollWorker.enqueueImmediate(context)
                }
                Result.success()
            }
            PushUploadResult.RETRY -> transientResult()
            PushUploadResult.AUTH_REQUIRED, PushUploadResult.REJECTED -> Result.success()
        }
    }

    private fun transientResult(): Result =
        if (runAttemptCount < MAX_RETRY_ATTEMPTS) Result.retry() else Result.success()

    companion object {
        private const val UNIQUE_WORK_NAME = "rolltop-push-subscription"
        private const val MAX_RETRY_ATTEMPTS = 3

        fun schedule(context: Context) {
            val request = OneTimeWorkRequest.Builder(PushSubscriptionWorker::class.java)
                .setConstraints(
                    Constraints.Builder()
                        .setRequiredNetworkType(NetworkType.CONNECTED)
                        .build()
                )
                .build()
            WorkManager.getInstance(context).enqueueUniqueWork(
                UNIQUE_WORK_NAME,
                ExistingWorkPolicy.REPLACE,
                request
            )
        }
    }
}
