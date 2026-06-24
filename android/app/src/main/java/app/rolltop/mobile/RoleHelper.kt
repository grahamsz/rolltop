package app.rolltop.mobile

import android.app.Activity
import android.content.Intent
import android.provider.Settings

object RoleHelper {
    fun requestDefaultMailRole(activity: Activity) {
        activity.startActivity(Intent(Settings.ACTION_MANAGE_DEFAULT_APPS_SETTINGS))
    }
}
