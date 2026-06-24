package app.rolltop.mobile

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.view.Gravity
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.WebChromeClient
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast

class MainActivity : Activity() {
    private var webView: WebView? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        NotificationChannels.ensure(this)
        NotificationPollReceiver.schedule(this)
        UpdateCheckReceiver.schedule(this)
        requestNotificationPermission()
        if (RolltopPrefs.serverUrl(this).isBlank()) showSetup() else showWeb(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        if (webView == null) showWeb(intent) else webView?.loadUrl(urlForIntent(intent))
    }

    private fun showSetup() {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
            setPadding(48, 48, 48, 48)
            layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
        }
        val title = TextView(this).apply {
            text = "Rolltop"
            textSize = 28f
        }
        val input = EditText(this).apply {
            hint = "https://mail.example.com"
            inputType = android.text.InputType.TYPE_TEXT_VARIATION_URI
            setSingleLine(true)
        }
        val connect = Button(this).apply {
            text = "Connect"
            setOnClickListener {
                RolltopPrefs.setServerUrl(this@MainActivity, input.text.toString())
                if (RolltopPrefs.serverUrl(this@MainActivity).isBlank()) {
                    Toast.makeText(this@MainActivity, "Enter your Rolltop server URL.", Toast.LENGTH_SHORT).show()
                } else {
                    showWeb(intent)
                }
            }
        }
        val role = Button(this).apply {
            text = "Set as default mail app"
            setOnClickListener { RoleHelper.requestDefaultMailRole(this@MainActivity) }
        }
        root.addView(title)
        root.addView(input, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        root.addView(connect)
        root.addView(role)
        setContentView(root)
    }

    private fun showWeb(sourceIntent: Intent?) {
        val view = WebView(this)
        webView = view
        CookieManager.getInstance().setAcceptCookie(true)
        CookieManager.getInstance().setAcceptThirdPartyCookies(view, true)
        view.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            databaseEnabled = true
            cacheMode = WebSettings.LOAD_DEFAULT
            mediaPlaybackRequiresUserGesture = true
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
        }
        view.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: android.webkit.WebResourceRequest): Boolean {
                val url = request.url.toString()
                if (url.startsWith("mailto:")) {
                    view.loadUrl(composeUrlFromMailto(request.url))
                    return true
                }
                return false
            }
        }
        view.webChromeClient = WebChromeClient()
        setContentView(view)
        view.loadUrl(urlForIntent(sourceIntent))
    }

    private fun urlForIntent(sourceIntent: Intent?): String {
        return when (sourceIntent?.action) {
            Intent.ACTION_SENDTO, Intent.ACTION_VIEW -> {
                val data = sourceIntent.data
                if (data?.scheme == "mailto") composeUrlFromMailto(data) else RolltopPrefs.buildUrl(this, "/mail")
            }
            Intent.ACTION_SEND, Intent.ACTION_SEND_MULTIPLE -> composeUrlFromShare(sourceIntent)
            else -> RolltopPrefs.buildUrl(this, "/mail")
        }
    }

    private fun composeUrlFromMailto(uri: Uri): String {
        val to = uri.schemeSpecificPart.substringBefore('?')
        val subject = uri.getQueryParameter("subject").orEmpty()
        val body = uri.getQueryParameter("body").orEmpty()
        return composeUrl(to = to, subject = subject, body = body)
    }

    private fun composeUrlFromShare(intent: Intent): String {
        val subject = intent.getStringExtra(Intent.EXTRA_SUBJECT).orEmpty()
        val text = intent.getStringExtra(Intent.EXTRA_TEXT).orEmpty()
        val stream = intent.getParcelableExtra<Uri>(Intent.EXTRA_STREAM)
        val body = if (stream != null && text.isBlank()) "Shared attachment: $stream" else text
        return composeUrl(subject = subject, body = body)
    }

    private fun composeUrl(to: String = "", subject: String = "", body: String = ""): String {
        val builder = Uri.parse(RolltopPrefs.buildUrl(this, "/compose")).buildUpon()
        if (to.isNotBlank()) builder.appendQueryParameter("to", to)
        if (subject.isNotBlank()) builder.appendQueryParameter("subject", subject)
        if (body.isNotBlank()) builder.appendQueryParameter("body", body)
        return builder.build().toString()
    }

    private fun requestNotificationPermission() {
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 44)
        }
    }
}
