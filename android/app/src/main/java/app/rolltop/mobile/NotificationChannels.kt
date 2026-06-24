package app.rolltop.mobile

import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.os.Build

object NotificationChannels {
    const val MAIL = "rolltop_mail"
    const val UPDATES = "rolltop_updates"

    fun ensure(context: Context) {
        if (Build.VERSION.SDK_INT < 26) return
        val manager = context.getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(
            NotificationChannel(MAIL, context.getString(R.string.notification_channel_mail), NotificationManager.IMPORTANCE_DEFAULT)
        )
        manager.createNotificationChannel(
            NotificationChannel(UPDATES, context.getString(R.string.notification_channel_updates), NotificationManager.IMPORTANCE_DEFAULT)
        )
    }
}
