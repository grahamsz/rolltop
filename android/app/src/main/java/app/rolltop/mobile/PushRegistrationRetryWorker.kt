package app.rolltop.mobile

import android.content.Context
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequest
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import org.unifiedpush.android.connector.UnifiedPush
import java.util.concurrent.TimeUnit

class PushRegistrationRetryWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    override fun doWork(): Result {
        val context = applicationContext
        val instance = NativePushStore.expectedInstance(context)
        val vapid = NativePushStore.vapid(context, instance)
        if (instance.isBlank() || vapid.isBlank() || UnifiedPush.getSavedDistributor(context) == null) {
            return Result.success()
        }
        return try {
            UnifiedPush.register(
                context = context,
                instance = instance,
                messageForDistributor = "Rolltop",
                vapid = vapid
            )
            Result.success()
        } catch (_: Exception) {
            if (runAttemptCount < MAX_WORK_RETRY_ATTEMPTS) Result.retry() else Result.success()
        }
    }

    companion object {
        private const val UNIQUE_WORK_NAME = "rolltop-push-registration-retry"
        private const val MAX_WORK_RETRY_ATTEMPTS = 3

        fun schedule(context: Context, delayMillis: Long) {
            val request = OneTimeWorkRequest.Builder(PushRegistrationRetryWorker::class.java)
                .setInitialDelay(delayMillis.coerceAtLeast(0), TimeUnit.MILLISECONDS)
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
