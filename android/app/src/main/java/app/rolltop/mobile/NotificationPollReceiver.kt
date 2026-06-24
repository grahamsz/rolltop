package app.rolltop.mobile

import android.app.AlarmManager
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.SystemClock
import androidx.core.app.NotificationCompat
import org.json.JSONArray
import org.json.JSONObject

class NotificationPollReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val pending = goAsync()
        Thread {
            try {
                poll(context.applicationContext)
            } finally {
                pending.finish()
            }
        }.start()
    }

    private fun poll(context: Context) {
        val base = RolltopPrefs.serverUrl(context)
        if (base.isBlank()) return
        val json = HttpJson.get(context, "$base/api/bootstrap") ?: return
        if (json.opt("user") == JSONObject.NULL) return
        val unread = unreadCount(json.optJSONArray("mailboxes"))
        val last = RolltopPrefs.lastUnread(context)
        RolltopPrefs.setLastUnread(context, unread)
        if (unread > last) showNotification(context, unread - last, unread)
    }

    private fun unreadCount(mailboxes: JSONArray?): Int {
        if (mailboxes == null) return 0
        var count = 0
        for (i in 0 until mailboxes.length()) {
            val mailbox = mailboxes.optJSONObject(i) ?: continue
            if (mailbox.optString("role").equals("inbox", ignoreCase = true) || mailbox.optString("name").equals("INBOX", ignoreCase = true)) {
                count += mailbox.optInt("unread_count", 0)
            }
        }
        return count
    }

    private fun showNotification(context: Context, newCount: Int, unread: Int) {
        NotificationChannels.ensure(context)
        val openIntent = PendingIntent.getActivity(
            context,
            100,
            Intent(context, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val text = if (newCount == 1) "1 new message" else "$newCount new messages"
        val notification = NotificationCompat.Builder(context, NotificationChannels.MAIL)
            .setSmallIcon(R.drawable.ic_rolltop)
            .setContentTitle("Rolltop")
            .setContentText("$text · $unread unread")
            .setContentIntent(openIntent)
            .setAutoCancel(true)
            .build()
        context.getSystemService(NotificationManager::class.java).notify(1001, notification)
    }

    companion object {
        fun schedule(context: Context) {
            val intent = Intent(context, NotificationPollReceiver::class.java)
            val pending = PendingIntent.getBroadcast(context, 200, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
            val alarm = context.getSystemService(AlarmManager::class.java)
            alarm.setInexactRepeating(
                AlarmManager.ELAPSED_REALTIME_WAKEUP,
                SystemClock.elapsedRealtime() + 60_000,
                15 * 60_000L,
                pending
            )
        }
    }
}
