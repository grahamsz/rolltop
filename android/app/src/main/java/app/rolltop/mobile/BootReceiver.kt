package app.rolltop.mobile

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        NotificationChannels.ensure(context)
        NotificationPollReceiver.schedule(context)
        UpdateCheckReceiver.schedule(context)
    }
}
