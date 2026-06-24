# Rolltop Android

This is a native Android wrapper for a Rolltop server. It gives Rolltop an installable Android app surface with WebView session persistence, notification polling, default mail app registration intents, Android share targets, and a self-update prompt flow.

## Build

```sh
gradle -p android :app:assembleDebug
```

CI builds the debug APK and uploads it as `rolltop-android-<version>`.

## First Run

Enter the HTTPS URL for the Rolltop server, then sign in through the embedded WebView. The app stores only the normalized server URL in shared preferences; Rolltop session cookies remain in Android WebView cookie storage.

## Default Mail App

The app declares `mailto:` handlers and uses `RoleManager.ROLE_EMAIL` on Android 10 and newer to request the default mail app role. Older Android versions fall back to the system default-apps settings screen.

Rolltop still does not send SMTP mail from the Android shell. Compose actions are routed to the Rolltop `/compose` page.

## Share Targets

Android `ACTION_SEND`, `ACTION_SEND_MULTIPLE`, `ACTION_SENDTO`, and `mailto:` links open Rolltop compose with the available subject/body fields prefilled. File streams are currently surfaced as attachment references in the body; uploading shared files to Rolltop compose should be handled as a separate compose API feature.

## Notifications

The app schedules an inexact 15-minute background poll against `/api/bootstrap` and shows a notification when the INBOX unread count increases. Android 13 and newer require the runtime notification permission.

Polling relies on the WebView login cookie for the configured Rolltop server. If the session expires, the app stops notifying until the user signs in again.

## Updates

The updater checks:

```text
<server-url>/android/latest.json
```

Expected metadata:

```json
{
  "versionCode": 2,
  "versionName": "0.2.0",
  "apkUrl": "https://mail.example.com/android/rolltop.apk",
  "sha256": "optional lowercase hex sha256"
}
```

When `versionCode` is newer than the installed app, the APK is downloaded, optionally verified with `sha256`, and exposed through the Android package installer.

Android does not allow a normal sideloaded app to silently replace itself. Users must confirm the install prompt, and may need to allow Rolltop to install unknown apps.
