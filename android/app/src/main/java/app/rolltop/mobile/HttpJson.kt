package app.rolltop.mobile

import android.content.Context
import android.webkit.CookieManager
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

object HttpJson {
    fun get(context: Context, url: String): JSONObject? {
        val conn = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"
            connectTimeout = 5_000
            readTimeout = 5_000
            setRequestProperty("Accept", "application/json")
            CookieManager.getInstance().getCookie(RolltopPrefs.serverUrl(context))?.let {
                setRequestProperty("Cookie", it)
            }
        }
        return try {
            if (conn.responseCode !in 200..299) return null
            JSONObject(conn.inputStream.bufferedReader().use { it.readText() })
        } catch (_: Exception) {
            null
        } finally {
            conn.disconnect()
        }
    }
}
