package app.rolltop.mobile

import org.unifiedpush.android.connector.FailedReason
import org.unifiedpush.android.connector.PushService
import org.unifiedpush.android.connector.data.PushEndpoint
import org.unifiedpush.android.connector.data.PushMessage

class RolltopPushService : PushService() {
    override fun onNewEndpoint(endpoint: PushEndpoint, instance: String) {
        val keys = endpoint.pubKeySet ?: return
        if (NativePushStore.saveEndpoint(this, instance, endpoint.url, keys.pubKey, keys.auth)) {
            PushSubscriptionWorker.schedule(this)
        }
    }

    override fun onMessage(message: PushMessage, instance: String) {
        if (!NativePushPolicy.acceptsMessage(NativePushStore.expectedInstance(this), instance, message.decrypted)) return
        NewMailPollWorker.enqueueImmediate(this)
    }

    override fun onRegistrationFailed(reason: FailedReason, instance: String) {
        if (instance == NativePushStore.expectedInstance(this) && NativePushPolicy.shouldRetryRegistration(reason.name)) {
            NativePushRegistration.retrySavedDistributor(this)
        }
    }

    override fun onTempUnavailable(instance: String) {
        if (instance == NativePushStore.expectedInstance(this)) {
            NativePushRegistration.retrySavedDistributor(this)
        }
    }

    override fun onUnregistered(instance: String) {
        NativePushStore.clearEndpoint(this, instance)
        if (instance == NativePushStore.expectedInstance(this)) {
            NativePushRegistration.retrySavedDistributor(this)
        }
    }
}
