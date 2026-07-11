package app.rolltop.mobile

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdatePolicyTest {
    @Test
    fun onlyNewerVersionCodesAreOffered() {
        assertTrue(UpdatePolicy.shouldOffer(12, 11))
        assertFalse(UpdatePolicy.shouldOffer(11, 11))
        assertFalse(UpdatePolicy.shouldOffer(10, 11))
    }

    @Test
    fun apkURLMustRemainOnConfiguredHTTPSOrigin() {
        assertEquals(
            "https://mail.example.test/android/rolltop.apk",
            UpdatePolicy.resolveAPKURL("https://mail.example.test", "/android/rolltop.apk")
        )
        assertNull(UpdatePolicy.resolveAPKURL("https://mail.example.test", "https://downloads.example.test/rolltop.apk"))
        assertNull(UpdatePolicy.resolveAPKURL("https://mail.example.test", "http://mail.example.test/rolltop.apk"))
        assertNull(UpdatePolicy.resolveAPKURL("https://mail.example.test", "https://user@mail.example.test/rolltop.apk"))
        assertNull(UpdatePolicy.resolveAPKURL("https://mail.example.test", ""))
    }

    @Test
    fun checksumIsEitherAbsentOrAFullSHA256() {
        assertTrue(UpdatePolicy.validSHA256(""))
        assertTrue(UpdatePolicy.validSHA256("a".repeat(64)))
        assertFalse(UpdatePolicy.validSHA256("abc123"))
    }

    @Test
    fun newerServerMetadataProducesAnInstallableOffer() {
        val offer = UpdatePolicy.parseOffer(
            "https://mail.example.test",
            installedVersionCode = 41,
            metadata = JSONObject(
                """{"versionCode":42,"versionName":"0.2.42","apkUrl":"/android/rolltop.apk","sha256":"${"a".repeat(64)}"}"""
            )
        )

        assertEquals(42, offer?.versionCode)
        assertEquals("0.2.42", offer?.versionName)
        assertEquals("https://mail.example.test/android/rolltop.apk", offer?.apkURL)
    }

    @Test
    fun serverMetadataCannotOfferCurrentOrCrossOriginBuilds() {
        assertNull(
            UpdatePolicy.parseOffer(
                "https://mail.example.test",
                42,
                JSONObject("""{"versionCode":42,"apkUrl":"/android/rolltop.apk"}""")
            )
        )
        assertNull(
            UpdatePolicy.parseOffer(
                "https://mail.example.test",
                41,
                JSONObject("""{"versionCode":42,"apkUrl":"https://other.example.test/rolltop.apk"}""")
            )
        )
    }
}
