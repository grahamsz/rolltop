package app.rolltop.mobile

import android.app.Activity
import android.app.role.RoleManager
import android.content.Context
import android.content.Intent
import android.os.Build
import android.provider.Settings

object RoleHelper {
    private const val REQUEST_ROLE_EMAIL = 501

    fun requestDefaultMailRole(activity: Activity) {
        if (Build.VERSION.SDK_INT >= 29) {
            val roleManager = activity.getSystemService(RoleManager::class.java)
            if (roleManager.isRoleAvailable(RoleManager.ROLE_EMAIL) && !roleManager.isRoleHeld(RoleManager.ROLE_EMAIL)) {
                activity.startActivityForResult(roleManager.createRequestRoleIntent(RoleManager.ROLE_EMAIL), REQUEST_ROLE_EMAIL)
                return
            }
        }
        activity.startActivity(Intent(Settings.ACTION_MANAGE_DEFAULT_APPS_SETTINGS))
    }

    fun isDefaultMailApp(context: Context): Boolean {
        if (Build.VERSION.SDK_INT < 29) return false
        val roleManager = context.getSystemService(RoleManager::class.java)
        return roleManager.isRoleAvailable(RoleManager.ROLE_EMAIL) && roleManager.isRoleHeld(RoleManager.ROLE_EMAIL)
    }
}
