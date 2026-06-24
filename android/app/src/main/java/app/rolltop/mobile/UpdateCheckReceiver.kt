package app.rolltop.mobile

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.SystemClock

class UpdateCheckReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val pending = goAsync()
        Thread {
            try {
                UpdateChecker.check(context.applicationContext)
            } finally {
                pending.finish()
            }
        }.start()
    }

    companion object {
        fun schedule(context: Context) {
            val intent = Intent(context, UpdateCheckReceiver::class.java)
            val pending = PendingIntent.getBroadcast(context, 300, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
            val alarm = context.getSystemService(AlarmManager::class.java)
            alarm.setInexactRepeating(
                AlarmManager.ELAPSED_REALTIME_WAKEUP,
                SystemClock.elapsedRealtime() + 10 * 60_000L,
                24 * 60 * 60_000L,
                pending
            )
        }
    }
}
