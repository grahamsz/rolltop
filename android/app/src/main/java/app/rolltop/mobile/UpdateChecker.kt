package app.rolltop.mobile

import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.core.app.NotificationCompat
import androidx.core.content.FileProvider
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest

object UpdateChecker {
    fun check(context: Context) {
        val base = RolltopPrefs.serverUrl(context)
        if (base.isBlank()) return
        val now = System.currentTimeMillis()
        if (now - RolltopPrefs.lastUpdateCheck(context) < 6 * 60 * 60_000L) return
        RolltopPrefs.setLastUpdateCheck(context, now)

        val metadata = HttpJson.get(context, "$base/android/latest.json") ?: return
        val versionCode = metadata.optInt("versionCode", 0)
        val apkUrl = metadata.optString("apkUrl", "")
        val sha256 = metadata.optString("sha256", "")
        if (versionCode <= BuildConfig.VERSION_CODE || apkUrl.isBlank()) return
        val apk = download(context, apkUrl) ?: return
        if (sha256.isNotBlank() && !sha256.equals(fileSha256(apk), ignoreCase = true)) {
            apk.delete()
            return
        }
        notifyInstall(context, apk, metadata)
    }

    private fun download(context: Context, rawUrl: String): File? {
        val conn = (URL(rawUrl).openConnection() as HttpURLConnection).apply {
            connectTimeout = 10_000
            readTimeout = 30_000
            setRequestProperty("Accept", "application/vnd.android.package-archive")
            instanceFollowRedirects = true
        }
        return try {
            if (conn.responseCode !in 200..299) return null
            val target = File(context.cacheDir, "rolltop-update.apk")
            conn.inputStream.use { input ->
                target.outputStream().use { output -> input.copyTo(output) }
            }
            target
        } catch (_: Exception) {
            null
        } finally {
            conn.disconnect()
        }
    }

    private fun notifyInstall(context: Context, apk: File, metadata: JSONObject) {
        NotificationChannels.ensure(context)
        val uri: Uri = FileProvider.getUriForFile(context, "${BuildConfig.APPLICATION_ID}.files", apk)
        val install = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        val pending = PendingIntent.getActivity(context, 400, install, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        val versionName = metadata.optString("versionName", "new version")
        val notification = NotificationCompat.Builder(context, NotificationChannels.UPDATES)
            .setSmallIcon(R.drawable.ic_rolltop)
            .setContentTitle("Rolltop update ready")
            .setContentText("Install $versionName")
            .setContentIntent(pending)
            .setAutoCancel(true)
            .build()
        context.getSystemService(NotificationManager::class.java).notify(2001, notification)
    }

    private fun fileSha256(file: File): String {
        val digest = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buffer = ByteArray(DEFAULT_BUFFER_SIZE)
            while (true) {
                val read = input.read(buffer)
                if (read <= 0) break
                digest.update(buffer, 0, read)
            }
        }
        return digest.digest().joinToString("") { "%02x".format(it) }
    }
}
