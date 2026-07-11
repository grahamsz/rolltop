package app.rolltop.mobile

internal object NativeSharePolicy {
    fun acceptsScheme(scheme: String?): Boolean = scheme.equals("content", ignoreCase = true)
}
