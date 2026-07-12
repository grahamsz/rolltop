package app.rolltop.mobile

import android.app.NotificationManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import androidx.work.Constraints
import androidx.work.Data
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequest
import androidx.work.OutOfQuotaPolicy
import androidx.work.WorkManager

class NotificationActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != ACTION_MARK_READ) return
        val messageIDs = NotificationActionPolicy.validMessageIDs(
            intent.getLongArrayExtra(EXTRA_MESSAGE_IDS) ?: longArrayOf()
        )
        val expectedUserID = intent.getLongExtra(NotificationIntentPolicy.EXTRA_EXPECTED_USER_ID, 0)
        val expectedServer = RolltopPrefs.normalizeBaseUrl(
            intent.getStringExtra(NotificationIntentPolicy.EXTRA_EXPECTED_SERVER_URL).orEmpty()
        )
        if (messageIDs.isEmpty() || expectedUserID <= 0 || expectedServer.isBlank()) return
        val notificationID = intent.getIntExtra(EXTRA_NOTIFICATION_ID, 0)
        if (notificationID in setOf(
                NotificationIntentPolicy.NEW_MAIL_NOTIFICATION_ID,
                NotificationIntentPolicy.SNOOZE_REMINDER_NOTIFICATION_ID
            )
        ) {
            context.getSystemService(NotificationManager::class.java).cancel(notificationID)
        }
        val data = Data.Builder()
            .putLongArray(NotificationActionWorker.KEY_MESSAGE_IDS, messageIDs)
            .putLong(NotificationActionWorker.KEY_EXPECTED_USER_ID, expectedUserID)
            .putString(NotificationActionWorker.KEY_EXPECTED_SERVER_URL, expectedServer)
            .build()
        val request = OneTimeWorkRequest.Builder(NotificationActionWorker::class.java)
            .setInputData(data)
            .setConstraints(
                Constraints.Builder()
                    .setRequiredNetworkType(NetworkType.CONNECTED)
                    .build()
            )
            .setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
            .build()
        WorkManager.getInstance(context).enqueueUniqueWork(
            UNIQUE_MARK_READ_WORK,
            ExistingWorkPolicy.APPEND_OR_REPLACE,
            request
        )
    }

    companion object {
        const val ACTION_MARK_READ = "app.rolltop.mobile.action.MARK_READ"
        const val EXTRA_MESSAGE_IDS = "app.rolltop.mobile.extra.MESSAGE_IDS"
        const val EXTRA_NOTIFICATION_ID = "app.rolltop.mobile.extra.NOTIFICATION_ID"
        private const val UNIQUE_MARK_READ_WORK = "rolltop-notification-mark-read"
    }
}
