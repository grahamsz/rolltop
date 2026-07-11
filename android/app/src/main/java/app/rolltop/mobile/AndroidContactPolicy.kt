package app.rolltop.mobile

import java.util.Locale

internal data class AndroidContactSuggestion(
    val name: String,
    val email: String
)

internal object AndroidContactPolicy {
    const val MAX_RESULTS = 8
    private const val MAX_QUERY_CHARS = 100
    private const val MAX_NAME_CHARS = 200
    private const val MAX_EMAIL_CHARS = 320

    fun normalizedQuery(raw: String): String {
        val query = raw.trim()
        if (query.length < 2) return ""
        return query.take(MAX_QUERY_CHARS)
    }

    fun normalizedSuggestions(rows: Iterable<AndroidContactSuggestion>): List<AndroidContactSuggestion> {
        val seen = mutableSetOf<String>()
        val result = mutableListOf<AndroidContactSuggestion>()
        for (row in rows) {
            val email = row.email.trim()
            if (email.isBlank() || email.length > MAX_EMAIL_CHARS || !email.contains('@')) continue
            if (!seen.add(email.lowercase(Locale.ROOT))) continue
            result += AndroidContactSuggestion(row.name.trim().take(MAX_NAME_CHARS), email)
            if (result.size == MAX_RESULTS) break
        }
        return result
    }
}
