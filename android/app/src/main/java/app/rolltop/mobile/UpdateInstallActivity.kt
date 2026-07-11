package app.rolltop.mobile

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.provider.Settings
import android.widget.Toast
import androidx.core.content.FileProvider

class UpdateInstallActivity : Activity() {
    private var openedPermissionSettings = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        openedPermissionSettings = savedInstanceState?.getBoolean(KEY_OPENED_SETTINGS) ?: false
    }

    override fun onResume() {
        super.onResume()
        val apk = UpdateChecker.validatedUpdateAPK(this)
        if (apk == null) {
            Toast.makeText(this, "The downloaded update is no longer available.", Toast.LENGTH_LONG).show()
            finish()
            return
        }
        if (packageManager.canRequestPackageInstalls()) {
            val uri = FileProvider.getUriForFile(this, "${BuildConfig.APPLICATION_ID}.files", apk)
            startActivity(Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(uri, "application/vnd.android.package-archive")
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            })
            finish()
            return
        }
        if (!openedPermissionSettings) {
            openedPermissionSettings = true
            startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")))
            return
        }
        Toast.makeText(this, "Allow Rolltop to install updates, then try again.", Toast.LENGTH_LONG).show()
        finish()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        outState.putBoolean(KEY_OPENED_SETTINGS, openedPermissionSettings)
        super.onSaveInstanceState(outState)
    }

    companion object {
        private const val KEY_OPENED_SETTINGS = "opened_permission_settings"
    }
}
