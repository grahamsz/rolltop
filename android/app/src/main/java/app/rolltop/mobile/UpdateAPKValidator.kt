package app.rolltop.mobile

import android.content.Context
import android.content.pm.PackageInfo
import android.content.pm.PackageManager
import android.content.pm.Signature
import android.os.Build
import androidx.core.content.pm.PackageInfoCompat
import java.io.File
import java.security.MessageDigest

object UpdateAPKValidator {
    fun validate(context: Context, apk: File, expectedVersionCode: Int): Boolean {
        if (!apk.isFile || expectedVersionCode <= 0) return false
        val archive = packageInfoForArchive(context.packageManager, apk) ?: return false
        if (archive.packageName != context.packageName) return false
        if (PackageInfoCompat.getLongVersionCode(archive) != expectedVersionCode.toLong()) return false
        val installed = installedPackageInfo(context.packageManager, context.packageName) ?: return false
        val installedSigners = signerSet(installed) ?: return false
        val candidateSigners = signerSet(archive) ?: return false
        return signingLineageCompatible(
            installedSigners.current,
            candidateSigners.current,
            candidateSigners.history,
            installedSigners.multiple,
            candidateSigners.multiple
        )
    }

    internal fun signingLineageCompatible(
        installedCurrent: Set<String>,
        candidateCurrent: Set<String>,
        candidateHistory: Set<String>,
        installedHasMultipleSigners: Boolean,
        candidateHasMultipleSigners: Boolean
    ): Boolean {
        if (installedCurrent.isEmpty() || candidateCurrent.isEmpty()) return false
        if (installedHasMultipleSigners || candidateHasMultipleSigners) {
            return installedHasMultipleSigners && candidateHasMultipleSigners && installedCurrent == candidateCurrent
        }
        return candidateHistory.containsAll(installedCurrent)
    }

    @Suppress("DEPRECATION")
    private fun packageInfoForArchive(packageManager: PackageManager, apk: File): PackageInfo? {
        val flags = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            PackageManager.GET_SIGNING_CERTIFICATES
        } else {
            PackageManager.GET_SIGNATURES
        }
        return try {
            packageManager.getPackageArchiveInfo(apk.absolutePath, flags)
        } catch (_: Exception) {
            null
        }
    }

    @Suppress("DEPRECATION")
    private fun installedPackageInfo(packageManager: PackageManager, packageName: String): PackageInfo? {
        val flags = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            PackageManager.GET_SIGNING_CERTIFICATES
        } else {
            PackageManager.GET_SIGNATURES
        }
        return try {
            packageManager.getPackageInfo(packageName, flags)
        } catch (_: Exception) {
            null
        }
    }

    @Suppress("DEPRECATION")
    private fun signerSet(info: PackageInfo): SignerSet? {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.P) {
            val digests = info.signatures.orEmpty().mapTo(linkedSetOf(), ::signatureDigest)
            return digests.takeIf { it.isNotEmpty() }?.let { SignerSet(it, it, it.size > 1) }
        }
        val signingInfo = info.signingInfo ?: return null
        val multiple = signingInfo.hasMultipleSigners()
        val current = signingInfo.apkContentsSigners.orEmpty().mapTo(linkedSetOf(), ::signatureDigest)
        val history = if (multiple) {
            current
        } else {
            signingInfo.signingCertificateHistory.orEmpty().mapTo(linkedSetOf(), ::signatureDigest).ifEmpty { current }
        }
        return SignerSet(current, history, multiple)
    }

    private fun signatureDigest(signature: Signature): String =
        MessageDigest.getInstance("SHA-256")
            .digest(signature.toByteArray())
            .joinToString("") { "%02x".format(it) }

    private data class SignerSet(
        val current: Set<String>,
        val history: Set<String>,
        val multiple: Boolean
    )
}
