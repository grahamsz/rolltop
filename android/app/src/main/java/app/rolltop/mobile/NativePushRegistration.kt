package app.rolltop.mobile

import android.app.Activity
import android.os.Build
import androidx.core.app.NotificationManagerCompat
import org.unifiedpush.android.connector.UnifiedPush
import java.lang.ref.WeakReference
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong

internal object NativePushRegistration {
    private const val NO_REGISTRATION = -1L
    private val generation = AtomicLong(0)
    private val attemptSequence = AtomicLong(0)
    private val registrationInFlightAttempt = AtomicLong(NO_REGISTRATION)
    private val bootstrapRetryCount = AtomicInteger(0)

    fun maybeRegister(activity: Activity) {
        if (!notificationsAllowed(activity)) return
        val base = RolltopPrefs.serverUrl(activity)
        if (base.isBlank()) return
        val lifecycleGeneration = generation.get()
        val attemptId = attemptSequence.incrementAndGet()
        if (!registrationInFlightAttempt.compareAndSet(NO_REGISTRATION, attemptId)) return

        Thread({
            val response = HttpJson.getResponse(activity, "$base/api/push/vapid-public-key")
            if (response == null || NativePushPolicy.uploadResult(response.statusCode) == PushUploadResult.RETRY) {
                finishAndRetry(activity, attemptId, lifecycleGeneration)
                return@Thread
            }
            val vapid = response.takeIf { it.statusCode in 200..299 }?.body
                ?.optString("public_key", "")
                .orEmpty()
            if (!NativePushPolicy.validVAPIDKey(vapid)) {
                finish(attemptId)
                return@Thread
            }
            val baseline = NewMailPollWorker.prepareForPushRegistration(activity)
            if (baseline.shouldRetry) {
                finishAndRetry(activity, attemptId, lifecycleGeneration)
                return@Thread
            }
            if (!baseline.authenticated || baseline.userId <= 0) {
                finish(attemptId)
                return@Thread
            }
            val instance = NativePushPolicy.instanceForServerUser(base, baseline.userId)
            if (instance.isBlank() || generation.get() != lifecycleGeneration ||
                RolltopPrefs.serverUrl(activity) != base || NativePushStore.expectedInstance(activity) != instance
            ) {
                finish(attemptId)
                return@Thread
            }
            bootstrapRetryCount.set(0)

            activity.runOnUiThread {
                if (activity.isFinishing || activity.isDestroyed) {
                    finish(attemptId)
                    return@runOnUiThread
                }
                if (generation.get() != lifecycleGeneration || RolltopPrefs.serverUrl(activity) != base ||
                    NativePushStore.expectedInstance(activity) != instance
                ) {
                    finish(attemptId)
                    return@runOnUiThread
                }
                val previous = NativePushStore.useInstance(activity, instance)
                if (previous != null) runCatching { UnifiedPush.unregister(activity, previous) }
                if (!NativePushStore.setVAPID(activity, instance, vapid)) {
                    finish(attemptId)
                    return@runOnUiThread
                }
                NativePushStore.resetRegistrationRetries(activity)
                PushSubscriptionWorker.schedule(activity)
                try {
                    UnifiedPush.tryUseCurrentOrDefaultDistributor(activity) { success ->
                        try {
                            if (success && generation.get() == lifecycleGeneration &&
                                RolltopPrefs.serverUrl(activity) == base && NativePushStore.expectedInstance(activity) == instance
                            ) {
                                UnifiedPush.register(
                                    context = activity,
                                    instance = instance,
                                    messageForDistributor = "Rolltop",
                                    vapid = vapid
                                )
                            }
                        } finally {
                            finish(attemptId)
                        }
                    }
                } catch (_: Exception) {
                    finish(attemptId)
                }
            }
        }, "rolltop-push-register").start()
    }

    fun unregister(context: android.content.Context) {
        generation.incrementAndGet()
        registrationInFlightAttempt.set(NO_REGISTRATION)
        bootstrapRetryCount.set(0)
        val instance = NativePushStore.clearAll(context)
        RolltopPrefs.clearNewMailCursor(context)
        if (instance == null) return
        runCatching { UnifiedPush.unregister(context, instance) }
    }

    fun clearForServerChange(context: android.content.Context) {
        unregister(context)
    }

    internal fun retrySavedDistributor(context: android.content.Context) {
        NativePushStore.nextRegistrationRetryDelay(context)?.let { delay ->
            PushRegistrationRetryWorker.schedule(context, delay)
        }
    }

    private fun finish(attemptId: Long) {
        registrationInFlightAttempt.compareAndSet(attemptId, NO_REGISTRATION)
    }

    private fun finishAndRetry(activity: Activity, attemptId: Long, lifecycleGeneration: Long) {
        finish(attemptId)
        if (generation.get() != lifecycleGeneration) return
        val delay = NativePushPolicy.registrationRetryDelay(bootstrapRetryCount.getAndIncrement()) ?: return
        val activityRef = WeakReference(activity)
        activity.runOnUiThread {
            activity.window.decorView.postDelayed({
                val target = activityRef.get()
                if (target != null && generation.get() == lifecycleGeneration &&
                    !target.isFinishing && !target.isDestroyed && target.hasWindowFocus()
                ) {
                    maybeRegister(target)
                }
            }, delay)
        }
    }

    private fun notificationsAllowed(activity: Activity): Boolean =
        (Build.VERSION.SDK_INT < 33 || activity.checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) ==
            android.content.pm.PackageManager.PERMISSION_GRANTED) &&
            NotificationManagerCompat.from(activity).areNotificationsEnabled()
}
