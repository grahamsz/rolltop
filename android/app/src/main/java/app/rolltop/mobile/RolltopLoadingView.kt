package app.rolltop.mobile

import android.animation.Animator
import android.animation.ValueAnimator
import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Path
import android.graphics.PathMeasure
import android.graphics.RectF
import android.util.AttributeSet
import android.view.View
import android.view.animation.Interpolator
import android.view.animation.LinearInterpolator
import android.view.animation.PathInterpolator
import kotlin.math.min

class RolltopLoadingView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
    defStyleAttr: Int = 0
) : View(context, attrs, defStyleAttr) {
    private val orangePaint = fillPaint(ORANGE)
    private val navyPaint = fillPaint(NAVY)
    private val archPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = ARCH
        style = Paint.Style.STROKE
        strokeWidth = ARCH_STROKE_WIDTH
        strokeCap = Paint.Cap.ROUND
        strokeJoin = Paint.Join.ROUND
    }
    private val wordmarkPaint = Paint(Paint.ANTI_ALIAS_FLAG or Paint.FILTER_BITMAP_FLAG)
    private val wordmark: Bitmap = BitmapFactory.decodeResource(resources, R.drawable.rolltop_logotype)
    private val topPath = Path()
    private val bottomPath = Path()
    private val archPath = createArchPath()
    private val visibleArchPath = Path()
    private val archMeasure = PathMeasure(archPath, false)
    private val wordmarkBounds = RectF()
    private val entranceEase: Interpolator = PathInterpolator(0.14f, 0.78f, 0.24f, 1f)
    private val shapeEase: Interpolator = PathInterpolator(0.2f, 0.8f, 0.2f, 1f)
    private val wordmarkEase: Interpolator = PathInterpolator(0.16f, 0.9f, 0.3f, 1.12f)

    private var animator: ValueAnimator? = null
    private var timelineProgress = 0f
    private var animationCancelled = false
    private var completionDelivered = false

    var onAnimationComplete: (() -> Unit)? = null
        set(value) {
            field = value
            if (completionDelivered) value?.invoke()
        }

    val animationsEnabled: Boolean
        get() = ValueAnimator.areAnimatorsEnabled()

    val elapsedMs: Long
        get() = (timelineProgress * DURATION_MS).toLong().coerceIn(0L, DURATION_MS)

    val isAnimationComplete: Boolean
        get() = completionDelivered

    init {
        setBackgroundColor(BACKGROUND)
        importantForAccessibility = IMPORTANT_FOR_ACCESSIBILITY_NO
    }

    fun start(startElapsedMs: Long = 0L) {
        animationCancelled = true
        cancelAnimator()
        animationCancelled = false
        completionDelivered = false

        val clampedElapsed = startElapsedMs.coerceIn(0L, DURATION_MS)
        timelineProgress = clampedElapsed.toFloat() / DURATION_MS
        invalidate()

        if (!animationsEnabled || clampedElapsed == DURATION_MS) {
            timelineProgress = 1f
            invalidate()
            deliverCompletion()
            return
        }

        val remainingMs = DURATION_MS - clampedElapsed
        val nextAnimator = ValueAnimator.ofFloat(timelineProgress, 1f)
        nextAnimator.duration = remainingMs
        nextAnimator.interpolator = LinearInterpolator()
        nextAnimator.addUpdateListener { valueAnimator ->
            timelineProgress = valueAnimator.animatedValue as Float
            invalidate()
        }
        nextAnimator.addListener(object : Animator.AnimatorListener {
            override fun onAnimationStart(animation: Animator) = Unit

            override fun onAnimationEnd(animation: Animator) {
                if (animationCancelled) return
                timelineProgress = 1f
                invalidate()
                deliverCompletion()
            }

            override fun onAnimationCancel(animation: Animator) = Unit

            override fun onAnimationRepeat(animation: Animator) = Unit
        })
        animator = nextAnimator
        nextAnimator.start()
    }

    fun cancel() {
        animationCancelled = true
        cancelAnimator()
    }

    override fun onDetachedFromWindow() {
        cancel()
        super.onDetachedFromWindow()
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        if (width == 0 || height == 0) return

        val scale = min(width / VIEWBOX_WIDTH, height / VIEWBOX_HEIGHT)
        val translateX = (width - VIEWBOX_WIDTH * scale) / 2f - VIEWBOX_LEFT * scale
        val translateY = (height - VIEWBOX_HEIGHT * scale) / 2f - VIEWBOX_TOP * scale
        val offscreenDistance = height * OFFSCREEN_HEIGHT_FRACTION / scale

        canvas.save()
        canvas.translate(translateX, translateY)
        canvas.scale(scale, scale)

        val topOffset = -offscreenDistance * (1f - phase(
            timelineProgress,
            TOP_ENTRANCE_START,
            TOP_ENTRANCE_END,
            entranceEase
        ))
        val bottomOffset = offscreenDistance * (1f - phase(
            timelineProgress,
            BOTTOM_ENTRANCE_START,
            BOTTOM_ENTRANCE_END,
            entranceEase
        ))
        val shapeProgress = phase(timelineProgress, SHAPE_START, SHAPE_END, shapeEase)

        buildTopPath(topPath, shapeProgress)
        canvas.save()
        canvas.translate(0f, topOffset)
        canvas.drawPath(topPath, orangePaint)
        canvas.restore()

        buildBottomPath(bottomPath, shapeProgress)
        canvas.save()
        canvas.translate(0f, bottomOffset)
        canvas.drawPath(bottomPath, navyPaint)
        canvas.restore()

        drawArch(canvas)
        drawWordmark(canvas)
        canvas.restore()
    }

    private fun drawArch(canvas: Canvas) {
        val alpha = phase(timelineProgress, ARCH_FADE_START, ARCH_DRAW_START, shapeEase)
        val drawProgress = phase(timelineProgress, ARCH_DRAW_START, ARCH_DRAW_END, shapeEase)
        if (alpha <= 0f || drawProgress <= 0f) return

        visibleArchPath.reset()
        archMeasure.getSegment(0f, archMeasure.length * drawProgress, visibleArchPath, true)
        archPaint.alpha = (alpha.coerceIn(0f, 1f) * 255f).toInt()
        canvas.drawPath(visibleArchPath, archPaint)
    }

    private fun drawWordmark(canvas: Canvas) {
        if (timelineProgress <= WORDMARK_START) return

        val scale: Float
        val alpha: Float
        if (timelineProgress < WORDMARK_PEAK) {
            val progress = phase(timelineProgress, WORDMARK_START, WORDMARK_PEAK, wordmarkEase)
            scale = lerp(WORDMARK_INITIAL_SCALE, WORDMARK_PEAK_SCALE, progress)
            alpha = progress
        } else {
            val progress = phase(timelineProgress, WORDMARK_PEAK, WORDMARK_END, wordmarkEase)
            scale = lerp(WORDMARK_PEAK_SCALE, 1f, progress)
            alpha = 1f
        }

        val centerX = WORDMARK_X + WORDMARK_WIDTH / 2f
        val centerY = WORDMARK_Y + WORDMARK_HEIGHT / 2f
        val halfWidth = WORDMARK_WIDTH * scale / 2f
        val halfHeight = WORDMARK_HEIGHT * scale / 2f
        wordmarkBounds.set(
            centerX - halfWidth,
            centerY - halfHeight,
            centerX + halfWidth,
            centerY + halfHeight
        )
        wordmarkPaint.alpha = (alpha.coerceIn(0f, 1f) * 255f).toInt()
        canvas.drawBitmap(wordmark, null, wordmarkBounds, wordmarkPaint)
    }

    private fun deliverCompletion() {
        if (completionDelivered || animationCancelled) return
        completionDelivered = true
        onAnimationComplete?.invoke()
    }

    private fun cancelAnimator() {
        animator?.cancel()
        animator = null
    }

    private fun phase(progress: Float, start: Float, end: Float, easing: Interpolator): Float {
        if (progress <= start) return 0f
        if (progress >= end) return 1f
        return easing.getInterpolation((progress - start) / (end - start))
    }

    private fun buildTopPath(path: Path, progress: Float) {
        path.reset()
        path.moveTo(33.03185f, lerp(-180f, 11.02356f, progress))
        path.cubicTo(
            21.91169f,
            lerp(-179.99976f, 11.02380f, progress),
            12.94011f,
            lerp(-172.10800f, 18.91556f, progress),
            11.30140f,
            lerp(-161.51069f, 29.51287f, progress)
        )
        path.lineTo(11.30140f, 29.51287f)
        path.lineTo(30.57464f, lerp(29.51287f, 44.40860f, progress))
        path.cubicTo(
            31.87797f,
            lerp(29.51287f, 45.41591f, progress),
            33.83279f,
            lerp(29.51287f, 45.41591f, progress),
            35.13612f,
            lerp(29.51287f, 44.40860f, progress)
        )
        path.lineTo(54.72200f, lerp(29.51287f, 29.27154f, progress))
        path.lineTo(54.72200f, lerp(-161.75202f, 29.27154f, progress))
        path.cubicTo(
            52.98563f,
            lerp(-172.22786f, 18.79570f, progress),
            44.06792f,
            lerp(-179.99976f, 11.02380f, progress),
            33.03237f,
            lerp(-180f, 11.02356f, progress)
        )
        path.close()
    }

    private fun buildBottomPath(path: Path, progress: Float) {
        path.reset()
        path.moveTo(54.72200f, lerp(35.01287f, 34.77154f, progress))
        path.lineTo(35.13612f, lerp(35.01287f, 49.90860f, progress))
        path.cubicTo(
            33.83279f,
            lerp(35.01287f, 50.91591f, progress),
            31.87797f,
            lerp(35.01287f, 50.91591f, progress),
            30.57464f,
            lerp(35.01287f, 49.90860f, progress)
        )
        path.lineTo(11.30140f, 35.01287f)
        path.lineTo(11.30140f, 35.01287f)
        path.lineTo(11.30140f, lerp(240f, 69.11513f, progress))
        path.lineTo(54.72200f, lerp(240f, 69.11513f, progress))
        path.lineTo(54.72200f, lerp(35.01287f, 34.77154f, progress))
        path.close()
    }

    private fun createArchPath() = Path().apply {
        moveTo(2.75000f, 66.36528f)
        lineTo(2.75400f, 36.91606f)
        lineTo(2.75400f, 36.91406f)
        lineTo(2.76000f, 33.02148f)
        cubicTo(2.76000f, 16.26402f, 16.27403f, 2.75000f, 33.03149f, 2.75000f)
        lineTo(33.03093f, 2.75000f)
        cubicTo(49.78839f, 2.75000f, 63.30241f, 16.26402f, 63.30241f, 33.02148f)
        lineTo(63.30841f, 36.91406f)
        lineTo(63.30841f, 36.91606f)
        lineTo(63.31641f, 66.36528f)
    }

    private fun fillPaint(colorValue: Int) = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = colorValue
        style = Paint.Style.FILL
    }

    private fun lerp(start: Float, end: Float, progress: Float): Float =
        start + (end - start) * progress

    companion object {
        const val DURATION_MS = 1_350L

        private const val LOCKUP_SCALE = 0.70f
        private const val VIEWBOX_CENTER_X = 33.033203f
        private const val VIEWBOX_CENTER_Y = 34.55764f
        private const val VIEWBOX_WIDTH = 126.066406f / LOCKUP_SCALE
        private const val VIEWBOX_HEIGHT = 289.11528f / LOCKUP_SCALE
        private const val VIEWBOX_LEFT = VIEWBOX_CENTER_X - VIEWBOX_WIDTH / 2f
        private const val VIEWBOX_TOP = VIEWBOX_CENTER_Y - VIEWBOX_HEIGHT / 2f
        private const val OFFSCREEN_HEIGHT_FRACTION = 0.70f

        private const val BOTTOM_ENTRANCE_START = 0.01f
        private const val BOTTOM_ENTRANCE_END = 0.10f
        private const val TOP_ENTRANCE_START = 0.06f
        private const val TOP_ENTRANCE_END = 0.24f
        private const val SHAPE_START = 0.24f
        private const val SHAPE_END = 0.43f
        private const val ARCH_FADE_START = 0.44f
        private const val ARCH_DRAW_START = 0.45f
        private const val ARCH_DRAW_END = 0.70f
        private const val WORDMARK_START = 0.72f
        private const val WORDMARK_PEAK = 0.82f
        private const val WORDMARK_END = 0.91f

        private const val WORDMARK_INITIAL_SCALE = 0.78f
        private const val WORDMARK_PEAK_SCALE = 1.055f
        private const val WORDMARK_X = 3.033f
        private const val WORDMARK_Y = 77.5f
        private const val WORDMARK_WIDTH = 60f
        private const val WORDMARK_HEIGHT = 17.419f
        private const val ARCH_STROKE_WIDTH = 5.5f

        private val BACKGROUND = Color.rgb(242, 240, 235)
        private val ORANGE = Color.rgb(196, 107, 68)
        private val ARCH = Color.rgb(197, 108, 69)
        private val NAVY = Color.rgb(21, 31, 46)
    }
}
