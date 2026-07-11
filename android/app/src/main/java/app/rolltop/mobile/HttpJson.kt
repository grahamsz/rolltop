package app.rolltop.mobile

import android.content.Context
import android.webkit.CookieManager
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

object HttpJson {
    fun get(
        context: Context,
        url: String,
        connectTimeoutMillis: Int = 5_000,
        readTimeoutMillis: Int = 5_000
    ): JSONObject? {
        val conn = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"
            connectTimeout = connectTimeoutMillis
            readTimeout = readTimeoutMillis
            instanceFollowRedirects = false
            useCaches = false
            setRequestProperty("Accept", "application/json")
            setRequestProperty("Cache-Control", "no-cache")
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
