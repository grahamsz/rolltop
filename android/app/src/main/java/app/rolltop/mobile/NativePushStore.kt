package app.rolltop.mobile

import android.content.Context

internal object NativePushStore {
    private const val NAME = "rolltop_native_push"
    private const val KEY_INSTANCE = "instance"
    private const val KEY_VAPID = "vapid"
    private const val KEY_ENDPOINT_VAPID = "endpoint_vapid"
    private const val KEY_ENDPOINT = "endpoint"
    private const val KEY_P256DH = "p256dh"
    private const val KEY_AUTH = "auth"
    private const val KEY_REGISTERED_ENDPOINT = "registered_endpoint"
    private const val KEY_OWNER_USER_ID = "owner_user_id"
    private const val KEY_RETRY_COUNT = "retry_count"

    @Synchronized
    fun useInstance(context: Context, instance: String): String? {
        if (instance.isBlank()) return null
        val preferences = preferences(context)
        val previous = preferences.getString(KEY_INSTANCE, "").orEmpty()
        if (previous == instance) return null
        preferences.edit()
            .putString(KEY_INSTANCE, instance)
            .remove(KEY_VAPID)
            .remove(KEY_ENDPOINT)
            .remove(KEY_ENDPOINT_VAPID)
            .remove(KEY_P256DH)
            .remove(KEY_AUTH)
            .remove(KEY_REGISTERED_ENDPOINT)
            .remove(KEY_OWNER_USER_ID)
            .remove(KEY_RETRY_COUNT)
            .commit()
        return previous.takeIf { it.isNotBlank() }
    }

    @Synchronized
    fun setVAPID(context: Context, instance: String, value: String): Boolean {
        if (!NativePushPolicy.validVAPIDKey(value)) return false
        val preferences = preferences(context)
        if (preferences.getString(KEY_INSTANCE, "") != instance) return false
        val editor = preferences.edit().putString(KEY_VAPID, value)
        if (preferences.getString(KEY_VAPID, "").orEmpty() != value) {
            editor.remove(KEY_ENDPOINT)
                .remove(KEY_ENDPOINT_VAPID)
                .remove(KEY_P256DH)
                .remove(KEY_AUTH)
                .remove(KEY_REGISTERED_ENDPOINT)
                .remove(KEY_OWNER_USER_ID)
        }
        return editor.commit()
    }

    fun vapid(context: Context, instance: String): String {
        val preferences = preferences(context)
        if (preferences.getString(KEY_INSTANCE, "") != instance) return ""
        return preferences.getString(KEY_VAPID, "").orEmpty().takeIf(NativePushPolicy::validVAPIDKey).orEmpty()
    }

    @Synchronized
    fun saveEndpoint(
        context: Context,
        instance: String,
        endpoint: String,
        p256dh: String,
        auth: String
    ): Boolean {
        val expected = expectedInstance(context)
        val subscription = NativePushPolicy.subscription(expected, instance, endpoint, p256dh, auth) ?: return false
        val vapid = vapid(context, instance)
        if (vapid.isBlank()) return false
        return preferences(context).edit()
            .putString(KEY_INSTANCE, subscription.instance)
            .putString(KEY_ENDPOINT, subscription.endpoint)
            .putString(KEY_ENDPOINT_VAPID, vapid)
            .putString(KEY_P256DH, subscription.p256dh)
            .putString(KEY_AUTH, subscription.auth)
            .remove(KEY_REGISTERED_ENDPOINT)
            .remove(KEY_RETRY_COUNT)
            .commit()
    }

    fun subscription(context: Context): NativePushSubscription? {
        val preferences = preferences(context)
        val instance = preferences.getString(KEY_INSTANCE, "").orEmpty()
        if (preferences.getString(KEY_ENDPOINT_VAPID, "").orEmpty() != vapid(context, instance)) return null
        return NativePushPolicy.subscription(
            expectedInstance = expectedInstance(context),
            instance = instance,
            endpoint = preferences.getString(KEY_ENDPOINT, "").orEmpty(),
            p256dh = preferences.getString(KEY_P256DH, "").orEmpty(),
            auth = preferences.getString(KEY_AUTH, "").orEmpty()
        )
    }

    fun ownerUserId(context: Context, instance: String): Long {
        val preferences = preferences(context)
        if (preferences.getString(KEY_INSTANCE, "") != instance) return 0
        return preferences.getLong(KEY_OWNER_USER_ID, 0).coerceAtLeast(0)
    }

    @Synchronized
    fun markUploaded(context: Context, saved: NativePushSubscription, userId: Long): Boolean {
        if (userId <= 0) return false
        val current = subscription(context)
        if (current != saved) return false
        return preferences(context).edit()
            .putString(KEY_REGISTERED_ENDPOINT, saved.endpoint)
            .putLong(KEY_OWNER_USER_ID, userId)
            .remove(KEY_RETRY_COUNT)
            .commit()
    }

    @Synchronized
    fun nextRegistrationRetryDelay(context: Context): Long? {
        val preferences = preferences(context)
        val count = preferences.getInt(KEY_RETRY_COUNT, 0).coerceAtLeast(0)
        val delay = NativePushPolicy.registrationRetryDelay(count) ?: return null
        return if (preferences.edit().putInt(KEY_RETRY_COUNT, count + 1).commit()) delay else null
    }

    fun resetRegistrationRetries(context: Context) {
        preferences(context).edit().remove(KEY_RETRY_COUNT).apply()
    }

    @Synchronized
    fun clearEndpoint(context: Context, instance: String) {
        val preferences = preferences(context)
        if (preferences.getString(KEY_INSTANCE, "") != instance) return
        preferences.edit()
            .remove(KEY_ENDPOINT)
            .remove(KEY_ENDPOINT_VAPID)
            .remove(KEY_P256DH)
            .remove(KEY_AUTH)
            .remove(KEY_REGISTERED_ENDPOINT)
            .remove(KEY_OWNER_USER_ID)
            .commit()
    }

    @Synchronized
    fun clearAll(context: Context): String? {
        val preferences = preferences(context)
        val instance = preferences.getString(KEY_INSTANCE, "").orEmpty()
        preferences.edit().clear().commit()
        return instance.takeIf { it.isNotBlank() }
    }

    fun expectedInstance(context: Context): String =
        NativePushPolicy.instanceForServerUser(
            RolltopPrefs.serverUrl(context),
            RolltopPrefs.newMailCursor(context)?.userId ?: 0
        )

    private fun preferences(context: Context) =
        context.getSharedPreferences(NAME, Context.MODE_PRIVATE)
}
