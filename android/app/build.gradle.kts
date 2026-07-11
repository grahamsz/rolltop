plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

val rolltopVersionCode = System.getenv("ROLLTOP_ANDROID_VERSION_CODE")?.toIntOrNull() ?: 2
val rolltopVersionName = System.getenv("ROLLTOP_ANDROID_VERSION_NAME")?.takeIf { it.isNotBlank() } ?: "0.2.0"
val releaseStorePath = System.getenv("ROLLTOP_ANDROID_KEYSTORE_FILE").orEmpty()
val releaseStorePassword = System.getenv("ROLLTOP_ANDROID_STORE_PASSWORD").orEmpty()
val releaseKeyAlias = System.getenv("ROLLTOP_ANDROID_KEY_ALIAS").orEmpty()
val releaseKeyPassword = System.getenv("ROLLTOP_ANDROID_KEY_PASSWORD").orEmpty()
val hasReleaseSigning = listOf(releaseStorePath, releaseStorePassword, releaseKeyAlias, releaseKeyPassword).all { it.isNotBlank() }

android {
    namespace = "app.rolltop.mobile"
    compileSdk = 35

    defaultConfig {
        applicationId = "app.rolltop.mobile"
        minSdk = 26
        targetSdk = 35
        versionCode = rolltopVersionCode
        versionName = rolltopVersionName
    }

    signingConfigs {
        if (hasReleaseSigning) {
            create("rolltopRelease") {
                storeFile = file(releaseStorePath)
                storePassword = releaseStorePassword
                keyAlias = releaseKeyAlias
                keyPassword = releaseKeyPassword
            }
        }
    }

    buildTypes {
        getByName("release") {
            isMinifyEnabled = false
            if (hasReleaseSigning) signingConfig = signingConfigs.getByName("rolltopRelease")
        }
    }

    buildFeatures {
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation("androidx.activity:activity:1.10.1")
    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.webkit:webkit:1.12.1")
    implementation("androidx.work:work-runtime:2.11.2")
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20240303")
}
