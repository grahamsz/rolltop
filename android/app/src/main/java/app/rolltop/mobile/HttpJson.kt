package app.rolltop.mobile

import android.content.Context
import android.webkit.CookieManager
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.nio.charset.StandardCharsets

internal data class HttpJsonResponse(val statusCode: Int, val body: JSONObject?)

object HttpJson {
    fun get(
        context: Context,
        url: String,
        connectTimeoutMillis: Int = 5_000,
        readTimeoutMillis: Int = 5_000
    ): JSONObject? = getResponse(context, url, connectTimeoutMillis, readTimeoutMillis)
        ?.takeIf { it.statusCode in 200..299 }
        ?.body

    internal fun getResponse(
        context: Context,
        url: String,
        connectTimeoutMillis: Int = 5_000,
        readTimeoutMillis: Int = 5_000
    ): HttpJsonResponse? = request(
        context = context,
        url = url,
        method = "GET",
        connectTimeoutMillis = connectTimeoutMillis,
        readTimeoutMillis = readTimeoutMillis
    )

    internal fun post(
        context: Context,
        url: String,
        body: JSONObject,
        csrfToken: String,
        connectTimeoutMillis: Int = 5_000,
        readTimeoutMillis: Int = 5_000
    ): HttpJsonResponse? = request(
        context = context,
        url = url,
        method = "POST",
        body = body,
        csrfToken = csrfToken,
        connectTimeoutMillis = connectTimeoutMillis,
        readTimeoutMillis = readTimeoutMillis
    )

    private fun request(
        context: Context,
        url: String,
        method: String,
        body: JSONObject? = null,
        csrfToken: String = "",
        connectTimeoutMillis: Int,
        readTimeoutMillis: Int
    ): HttpJsonResponse? {
        val serverBase = RolltopPrefs.serverUrl(context)
        if (serverBase.isBlank() || RolltopPrefs.internalLocation(serverBase, url) == null) return null
        val conn = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = method
            connectTimeout = connectTimeoutMillis
            readTimeout = readTimeoutMillis
            instanceFollowRedirects = false
            useCaches = false
            setRequestProperty("Accept", "application/json")
            setRequestProperty("Cache-Control", "no-cache")
            CookieManager.getInstance().getCookie(serverBase)?.let {
                setRequestProperty("Cookie", it)
            }
            if (csrfToken.isNotBlank()) setRequestProperty("X-CSRF-Token", csrfToken)
            if (body != null) {
                doOutput = true
                setRequestProperty("Content-Type", "application/json; charset=utf-8")
            }
        }
        return try {
            if (body != null) {
                conn.outputStream.use { output ->
                    output.write(body.toString().toByteArray(StandardCharsets.UTF_8))
                }
            }
            val status = conn.responseCode
            if (RolltopPrefs.serverUrl(context) == serverBase) {
                conn.headerFields.entries
                    .filter { (name, _) -> name.equals("Set-Cookie", ignoreCase = true) }
                    .flatMap { it.value.orEmpty() }
                    .forEach { CookieManager.getInstance().setCookie(serverBase, it) }
                CookieManager.getInstance().flush()
            }
            val stream = if (status in 200..299) conn.inputStream else conn.errorStream
            val raw = stream?.bufferedReader()?.use { it.readText() }.orEmpty()
            val parsed = try {
                raw.takeIf { it.isNotBlank() }?.let(::JSONObject)
            } catch (_: Exception) {
                null
            }
            HttpJsonResponse(status, parsed)
        } catch (_: Exception) {
            null
        } finally {
            conn.disconnect()
        }
    }
}
