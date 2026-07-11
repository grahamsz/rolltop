package app.rolltop.mobile

import android.Manifest
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.net.Uri
import android.net.http.SslError
import android.os.Build
import android.os.Bundle
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.SslErrorHandler
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.OnBackPressedCallback
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.lifecycle.Lifecycle
import androidx.webkit.ServiceWorkerClientCompat
import androidx.webkit.ServiceWorkerControllerCompat
import androidx.webkit.WebViewFeature

class MainActivity : ComponentActivity() {
    private var webView: WebView? = null
    private var androidWebBridge: AndroidWebBridge? = null
    private var rolltopWebChromeClient: RolltopWebChromeClient? = null
    private val nativeShareStore by lazy { NativeShareStore(applicationContext) }
    private var updatePromptPolicy = UpdatePromptPolicy()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        updatePromptPolicy = UpdatePromptPolicy(savedInstanceState?.getInt(STATE_PROMPTED_UPDATE_CODE) ?: 0)
        installBackNavigation()
        WindowCompat.setDecorFitsSystemWindows(window, false)
        WindowInsetsControllerCompat(window, window.decorView).apply {
            isAppearanceLightStatusBars = true
            isAppearanceLightNavigationBars = true
        }
        NotificationChannels.ensure(this)
        NewMailPollWorker.schedule(this)
        UpdateCheckWorker.schedule(this)
        requestNotificationPermission()
        if (RolltopPrefs.serverUrl(this).isBlank()) {
            showSetup()
        } else {
            showWeb(intent, savedInstanceState?.getBundle(STATE_WEB_VIEW))
        }
    }

    override fun onStart() {
        super.onStart()
        checkForServerUpdate()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        if (webView == null) {
            if (RolltopPrefs.serverUrl(this).isBlank()) showSetup() else showWeb(intent)
        } else {
            explicitUrlForIntent(intent)?.let { target ->
                webView?.loadUrl(target)
                consumeNavigationIntent()
            }
        }
    }

    override fun onPause() {
        rememberCurrentLocation()
        CookieManager.getInstance().flush()
        super.onPause()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        rememberCurrentLocation()
        outState.putInt(STATE_PROMPTED_UPDATE_CODE, updatePromptPolicy.lastPromptedVersionCode)
        webView?.let { view ->
            val state = Bundle()
            view.saveState(state)
            outState.putBundle(STATE_WEB_VIEW, state)
        }
        super.onSaveInstanceState(outState)
    }

    override fun onDestroy() {
        androidWebBridge = null
        rolltopWebChromeClient?.cancelPendingRequest()
        rolltopWebChromeClient = null
        webView?.destroy()
        webView = null
        super.onDestroy()
    }

    @Deprecated("Android contact picking uses the platform activity result callback.")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        if (androidWebBridge?.handleActivityResult(requestCode, resultCode, data) == true) return
        if (rolltopWebChromeClient?.handleActivityResult(requestCode, resultCode, data) == true) return
        super.onActivityResult(requestCode, resultCode, data)
    }

    private fun showSetup(initialUrl: String = RolltopPrefs.serverUrl(this), message: String = "") {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
            setBackgroundColor(SHELL_BACKGROUND)
            setPadding(dp(24), dp(24), dp(24), dp(24))
            layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
        }
        val title = TextView(this).apply {
            text = "Rolltop"
            textSize = 28f
        }
        val feedback = TextView(this).apply {
            text = message
            setTextColor(Color.rgb(151, 43, 43))
            visibility = if (message.isBlank()) View.GONE else View.VISIBLE
        }
        val input = EditText(this).apply {
            hint = "https://mail.example.com"
            inputType = android.text.InputType.TYPE_CLASS_TEXT or android.text.InputType.TYPE_TEXT_VARIATION_URI
            setSingleLine(true)
            setText(initialUrl)
            setSelection(text.length)
        }
        val connect = Button(this).apply { text = "Connect" }
        connect.setOnClickListener {
            val normalized = RolltopPrefs.normalizeBaseUrl(input.text.toString())
            if (normalized.isEmpty()) {
                input.error = "Enter a valid HTTPS Rolltop server URL."
                input.requestFocus()
                return@setOnClickListener
            }

            input.isEnabled = false
            connect.isEnabled = false
            connect.text = "Checking..."
            feedback.setTextColor(Color.rgb(74, 81, 78))
            feedback.text = "Checking the Rolltop server..."
            feedback.visibility = View.VISIBLE

            Thread({
                val result = RolltopServerValidator.validate(normalized)
                runOnUiThread {
                    if (isFinishing || isDestroyed) return@runOnUiThread
                    input.isEnabled = true
                    connect.isEnabled = true
                    connect.text = "Connect"
                    if (!result.valid) {
                        feedback.setTextColor(Color.rgb(151, 43, 43))
                        feedback.text = result.message
                        return@runOnUiThread
                    }
                    RolltopPrefs.setServerUrl(this@MainActivity, normalized)
                    showWeb(intent)
                }
            }, "rolltop-server-check").start()
        }
        val role = Button(this).apply {
            text = "Set as default mail app"
            setOnClickListener { RoleHelper.requestDefaultMailRole(this@MainActivity) }
        }
        root.addView(title, spacedLayoutParams(top = 0, bottom = 12))
        root.addView(feedback, spacedLayoutParams(bottom = 12))
        root.addView(input, spacedLayoutParams(bottom = 8))
        root.addView(connect, spacedLayoutParams(bottom = 4))
        root.addView(role, spacedLayoutParams())
        applySystemBarInsets(root, dp(24))
        setContentView(root)
    }

    private fun showWeb(sourceIntent: Intent?, restoredState: Bundle? = null) {
        val view = WebView(this)
        webView = view
        val serverOrigin = RolltopPrefs.serverUrl(this)
        installNativeShareServiceWorkerInterceptor(serverOrigin)
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
            private var mainFrameCommitted = false
            private var failureHandled = false

            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val url = request.url.toString()
                if (url.startsWith("mailto:")) {
                    view.loadUrl(composeUrlFromMailto(request.url))
                    return true
                }
                if ((request.url.scheme == "http" || request.url.scheme == "https") &&
                    RolltopPrefs.internalLocation(RolltopPrefs.serverUrl(this@MainActivity), url) == null
                ) {
                    try {
                        startActivity(Intent(Intent.ACTION_VIEW, request.url))
                    } catch (_: ActivityNotFoundException) {
                        Toast.makeText(this@MainActivity, "No browser can open this link.", Toast.LENGTH_SHORT).show()
                    }
                    failInitialLoad(view, "The server redirected outside the configured Rolltop address.")
                    return true
                }
                return false
            }

            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? =
                nativeShareStore.intercept(request, serverOrigin) ?: super.shouldInterceptRequest(view, request)

            override fun onPageCommitVisible(view: WebView, url: String) {
                mainFrameCommitted = true
                RolltopPrefs.rememberVisitedUrl(this@MainActivity, url)
            }

            override fun onPageFinished(view: WebView, url: String) {
                RolltopPrefs.rememberVisitedUrl(this@MainActivity, url)
            }

            override fun doUpdateVisitedHistory(view: WebView, url: String, isReload: Boolean) {
                RolltopPrefs.rememberVisitedUrl(this@MainActivity, url)
            }

            override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
                if (request.isForMainFrame) {
                    failInitialLoad(view, "Could not connect to this Rolltop server. Check the address and try again.")
                }
            }

            override fun onReceivedHttpError(view: WebView, request: WebResourceRequest, errorResponse: WebResourceResponse) {
                if (request.isForMainFrame && errorResponse.statusCode >= 400) {
                    failInitialLoad(view, "The server did not recognize Rolltop at this address. Check the URL and try again.")
                }
            }

            override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, error: SslError) {
                handler.cancel()
                failInitialLoad(view, "Rolltop could not verify this server's HTTPS certificate. Check the URL and certificate, then try again.")
            }

            private fun failInitialLoad(view: WebView, message: String) {
                if (mainFrameCommitted || failureHandled) return
                failureHandled = true
                view.post { showConnectionFailure(view, message) }
            }
        }
        rolltopWebChromeClient = RolltopWebChromeClient(this).also { view.webChromeClient = it }
        androidWebBridge = AndroidWebBridge(this, view, serverOrigin, nativeShareStore).also { it.attach() }
        val root = FrameLayout(this).apply {
            setBackgroundColor(Color.WHITE)
            addView(view, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        }
        applySystemBarInsets(root)
        setContentView(root)
        if (lifecycle.currentState.isAtLeast(Lifecycle.State.STARTED)) checkForServerUpdate()

        val explicitTarget = explicitUrlForIntent(sourceIntent)
        val restored = explicitTarget == null && restoredState != null && view.restoreState(restoredState) != null
        if (!restored) view.loadUrl(explicitTarget ?: urlForIntent(sourceIntent))
        if (explicitTarget != null) consumeNavigationIntent()
    }

    private fun urlForIntent(sourceIntent: Intent?): String {
        return explicitUrlForIntent(sourceIntent)
            ?: RolltopPrefs.lastVisitedUrl(this).takeIf { it.isNotBlank() }
            ?: RolltopPrefs.buildUrl(this, "/mail")
    }

    private fun explicitUrlForIntent(sourceIntent: Intent?): String? {
        RolltopPrefs.resolveInternalUrl(
            this,
            sourceIntent?.getStringExtra(NewMailPollWorker.EXTRA_TARGET_PATH)
        )?.let { return it }

        return when (sourceIntent?.action) {
            Intent.ACTION_SENDTO, Intent.ACTION_VIEW -> {
                val data = sourceIntent.data
                if (data?.scheme == "mailto") composeUrlFromMailto(data) else null
            }
            Intent.ACTION_SEND, Intent.ACTION_SEND_MULTIPLE -> composeUrlFromShare(sourceIntent)
            else -> null
        }
    }

    private fun showConnectionFailure(failedView: WebView, message: String) {
        if (webView !== failedView || isFinishing || isDestroyed) return
        webView = null
        failedView.stopLoading()
        showSetup(RolltopPrefs.serverUrl(this), message)
        failedView.destroy()
    }

    private fun rememberCurrentLocation() {
        RolltopPrefs.rememberVisitedUrl(this, webView?.url)
    }

    private fun checkForServerUpdate() {
        if (RolltopPrefs.serverUrl(this).isBlank()) return
        UpdateChecker.checkInForeground(this, force = true, shouldPrompt = updatePromptPolicy::shouldPrompt)
    }

    private fun installNativeShareServiceWorkerInterceptor(serverOrigin: String) {
        if (!WebViewFeature.isFeatureSupported(WebViewFeature.SERVICE_WORKER_BASIC_USAGE) ||
            !WebViewFeature.isFeatureSupported(WebViewFeature.SERVICE_WORKER_SHOULD_INTERCEPT_REQUEST)
        ) return
        // Fetches owned by an active service worker bypass the page WebViewClient.
        ServiceWorkerControllerCompat.getInstance().setServiceWorkerClient(object : ServiceWorkerClientCompat() {
            override fun shouldInterceptRequest(request: WebResourceRequest): WebResourceResponse? =
                nativeShareStore.intercept(request, serverOrigin)
        })
    }

    private fun installBackNavigation() {
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                val view = webView
                if (view?.canGoBack() == true) {
                    view.goBack()
                    return
                }
                AndroidBackNavigation.messageFallbackUrl(RolltopPrefs.serverUrl(this@MainActivity), view?.url)
                    ?.let {
                        view?.loadUrl(it)
                        return
                    }

                isEnabled = false
                onBackPressedDispatcher.onBackPressed()
            }
        })
    }

    private fun consumeNavigationIntent() {
        setIntent(Intent(this, MainActivity::class.java).setAction(Intent.ACTION_MAIN))
    }

    private fun applySystemBarInsets(view: View, contentPadding: Int = 0) {
        ViewCompat.setOnApplyWindowInsetsListener(view) { target, insets ->
            val safe = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout())
            val keyboard = insets.getInsets(WindowInsetsCompat.Type.ime())
            target.setPadding(
                contentPadding + safe.left,
                contentPadding + safe.top,
                contentPadding + safe.right,
                contentPadding + maxOf(safe.bottom, keyboard.bottom)
            )
            insets
        }
        ViewCompat.requestApplyInsets(view)
    }

    private fun spacedLayoutParams(top: Int = 0, bottom: Int = 0) =
        LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            topMargin = dp(top)
            bottomMargin = dp(bottom)
        }

    private fun dp(value: Int): Int = (value * resources.displayMetrics.density).toInt()

    private fun composeUrlFromMailto(uri: Uri): String {
        val to = uri.schemeSpecificPart.substringBefore('?')
        val subject = uri.getQueryParameter("subject").orEmpty()
        val body = uri.getQueryParameter("body").orEmpty()
        return composeUrl(to = to, subject = subject, body = body)
    }

    private fun composeUrlFromShare(intent: Intent): String {
        val subject = intent.getStringExtra(Intent.EXTRA_SUBJECT).orEmpty()
        val text = intent.getStringExtra(Intent.EXTRA_TEXT).orEmpty()
        return composeUrl(subject = subject, body = text, nativeShareID = nativeShareStore.capture(intent).orEmpty())
    }

    private fun composeUrl(
        to: String = "",
        subject: String = "",
        body: String = "",
        nativeShareID: String = ""
    ): String {
        val builder = Uri.parse(RolltopPrefs.buildUrl(this, "/compose")).buildUpon()
        if (to.isNotBlank()) builder.appendQueryParameter("to", to)
        if (subject.isNotBlank()) builder.appendQueryParameter("subject", subject)
        if (body.isNotBlank()) builder.appendQueryParameter("body", body)
        if (nativeShareID.isNotBlank()) builder.appendQueryParameter("android_share", nativeShareID)
        return builder.build().toString()
    }

    private fun requestNotificationPermission() {
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 44)
        }
    }

    companion object {
        private const val STATE_WEB_VIEW = "rolltop.web_view_state"
        private const val STATE_PROMPTED_UPDATE_CODE = "rolltop.prompted_update_code"
        private val SHELL_BACKGROUND = Color.rgb(242, 240, 235)
    }
}
