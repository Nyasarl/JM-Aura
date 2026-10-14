import { memo, useCallback, useEffect, useRef, useState } from 'react'
import Box from '@mui/material/Box'
import CircularProgress from '@mui/material/CircularProgress'
import Typography from '@mui/material/Typography'
import BrokenImageIcon from '@mui/icons-material/BrokenImage'
import { md5 } from '../md5'

// 阅读器图片组件（双通道）：
//
// 默认在**本机**用 canvas 完成 JM 分块乱序还原——解读码这部分 CPU 由用户自己的设备承担，
// 服务端只负责原样转发。只有当本机还原确实做不到时才回退到服务端反置乱
// （/api/chapter_image?scramble=...），也就是「前端优先、失败才上服务端」。
//
// 为什么需要回退：iOS Safari 对 canvas 总内存有硬上限（约设备内存 1/4），超限后
// getContext 返回 null 或画布被静默清空。这里用三个机制把失败概率压到最低：
//   1) 视口带宽度按屏高自适应，带外的页面立刻交回位图（手机不会同时持有五六张长条漫画面）；
//   2) 再加一层字节预算（张数 + 总字节数），超预算时只回收「离视口最远且已离开视口带」的画布；
//   3) 还原后读回一个像素校验，发现画布已被清空就判定失败。
// 一旦本设备连续失败，后续图片直接走服务端（熔断），避免每张图都白试一次。

// 设备级熔断状态：模块作用域，同一页面会话内共享。
let canvasFailCount = 0
let canvasDisabled = false
const CANVAS_FAIL_LIMIT = 2

// 换章时调用：一次偶发失败（某张图损坏等）不该让之后所有图片都改走服务端，
// 否则阅读链路的 CPU 又会全部压回后端。复位后最多再试 CANVAS_FAIL_LIMIT 张。
export function resetCanvasAvailability() {
  canvasFailCount = 0
  canvasDisabled = false
}

// 已解码画布列表（模块级），超出预算时释放，避免逼近 iOS canvas 总内存上限。
// 只限张数挡不住长条漫：单张 800×5000 就是 16MB，8 张就到 128MB，所以张数与总字节一起卡。
//
// 最关键的约束：**只回收已经离开视口带的画布**。因为「离开视口带就交回位图、回到视口带才重解码」
// 与这里用的 `active()` 是同一个判定，回收与重解码天然互斥，不会出现
// 「释放 A → A 仍在视口带内 → 立刻重解码 → 又超预算 → 再释放 A」这种自我激发的死循环。
// （旧实现「优先逐出离开视口的，一个都没有就逐出第一个」正好会逐出仍带内的画布，
//   于是读到后面几页就进入反复解码/建画布的抖动，在 iOS 上表现为画面错乱、越读越卡。）
interface LiveCanvas {
  release: () => void
  /** 是否仍在视口带内（与释放/重解码同一个信号）。 */
  active: () => boolean
  /** 与视口的垂直距离（px），越大越先被回收。 */
  distance: () => number
  /** 位图字节数（w*h*4），用于总量预算。 */
  bytes: number
}
const liveCanvases: LiveCanvas[] = []
const MAX_LIVE_CANVASES = 4
const MAX_LIVE_CANVAS_BYTES = 32 * 1024 * 1024

// 明确标记「这张 canvas 已被丢弃」。位图统一放在 effect cleanup 里清空，
// 而 Dev 的 StrictMode 会把 effect 跑成 mount→cleanup→mount：
// 这种情况下同一张 canvas 会被重新挂回来，若 cleanup 无条件置零就会看到空白页。
const discardedCanvases = new WeakSet<HTMLCanvasElement>()

/** 元素与视口的垂直距离：0 表示正与视口相交。 */
function viewportDistance(el: Element | null): number {
  if (!el || typeof el.getBoundingClientRect !== 'function') return Number.MAX_SAFE_INTEGER
  const rect = el.getBoundingClientRect()
  const vh = window.innerHeight || 0
  if (rect.bottom < 0) return -rect.bottom
  if (rect.top > vh) return rect.top - vh
  return 0
}

/** 视口带宽度：按屏高自适应而不是写死像素。
 * 手机上一屏 800 多 px，1.2 屏就够提前解码；写死 3000px 会让手机同时持有五六张
 * 长条漫画面（最坏 ≈100MB），这正是 iOS canvas 内存被顶爆、画面错乱的来源。 */
function bandMargin(): string {
  const vh = typeof window === 'undefined' ? 800 : window.innerHeight || 800
  return `${Math.round(Math.min(2400, Math.max(900, vh * 1.2)))}px 0px`
}

function retainCanvas(canvas: LiveCanvas) {
  liveCanvases.push(canvas)
  let bytes = liveCanvases.reduce((n, c) => n + c.bytes, 0)
  while (liveCanvases.length > MAX_LIVE_CANVASES || bytes > MAX_LIVE_CANVAS_BYTES) {
    let victimIndex = -1
    let worst = -1
    for (let i = 0; i < liveCanvases.length; i++) {
      const c = liveCanvases[i]
      // 刚挂上的这张、以及仍在视口带内的图都不回收。一个可回收的都找不到时
      // 宁可暂时超出预算（张数已被视口带兜住），也不能把用户正在看的页面逐出去。
      if (c === canvas || c.active()) continue
      const d = c.distance()
      if (d > worst) {
        worst = d
        victimIndex = i
      }
    }
    if (victimIndex < 0) break
    const victim = liveCanvases[victimIndex]
    liveCanvases.splice(victimIndex, 1)
    bytes -= victim.bytes
    victim.release()
  }
}

function dropCanvas(release: () => void) {
  const i = liveCanvases.findIndex((c) => c.release === release)
  if (i >= 0) liveCanvases.splice(i, 1)
}

function noteCanvasFailure() {
  canvasFailCount += 1
  if (canvasFailCount >= CANVAS_FAIL_LIMIT) canvasDisabled = true
}

// 全部图片共用一个 IntersectionObserver：长章节会挂载很多张图，
// 每张各建一个观察器（并在卸载时销毁）没必要，共用一个只需一次注册开销。
const ioCallbacks = new WeakMap<Element, (visible: boolean) => void>()
let sharedObserver: IntersectionObserver | null = null

function observeVisibility(el: Element, cb: (visible: boolean) => void): () => void {
  if (typeof IntersectionObserver === 'undefined') {
    cb(true)
    return () => {}
  }
  if (!sharedObserver) {
    sharedObserver = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          ioCallbacks.get(entry.target)?.(entry.isIntersecting)
        }
      },
      { rootMargin: bandMargin() },
    )
  }
  ioCallbacks.set(el, cb)
  sharedObserver.observe(el)
  return () => {
    sharedObserver?.unobserve(el)
    ioCallbacks.delete(el)
  }
}

function isGif(url: string): boolean {
  return url.toLowerCase().includes('.gif')
}

// 与后端解码及上游实现完全一致的切片口径（220980 / 268850 / 421926 三个阈值不可改）。
export function getSegmentationNum(
  epsId: string,
  scrambleId: string,
  pictureName: string,
): number {
  const sid = parseInt(scrambleId, 10) || 220980
  const eid = parseInt(epsId, 10)
  if (isNaN(eid)) return 0
  if (eid < sid) return 0
  if (eid < 268850) return 10
  const keyCode = md5(String(eid) + String(pictureName)).charCodeAt(32 - 1)
  if (eid > 421926) {
    return (keyCode % 8) * 2 + 2
  }
  return (keyCode % 10) * 2 + 2
}

interface DscImageProps {
  /** 原图地址（服务端原样转发，供本机 canvas 还原）。 */
  src: string
  /** 服务端已还原好的地址（兜底通道），缺省表示没有兜底可用。 */
  serverSrc?: string
  comicId: string
  scrambleId: string
  index?: number
  /** 超过该序号的图片进入视口后才真正加载（阅读器性能门控） */
  lazyAfter?: number
  /** 阅读器内解除 900px 上限，跟随容器宽度设置 */
  fullWidth?: boolean
}

type Phase = 'idle' | 'loading' | 'error' | 'done'

function DscImage({ src, serverSrc, comicId, scrambleId, index = 0, lazyAfter, fullWidth }: DscImageProps) {
  const [phase, setPhase] = useState<Phase>('idle')
  const [displaySrc, setDisplaySrc] = useState('')
  const [canvasEl, setCanvasEl] = useState<HTMLCanvasElement | null>(null)
  const [imgKey, setImgKey] = useState(0)
  const [visible, setVisible] = useState(false)
  // 本图是否已改走服务端兜底通道（本机还原失败或设备已熔断）。
  const [useServer, setUseServer] = useState(false)
  // 原图尺寸：用于生成与真实渲染等高的 aspect-ratio 占位，加载/释放画布时滚动几何完全不变。
  const [natW, setNatW] = useState(0)
  const [natH, setNatH] = useState(0)

  const holderRef = useRef<HTMLDivElement | null>(null)
  const canvasContainerRef = useRef<HTMLDivElement | null>(null)
  const loadTokenRef = useRef(0)
  const retriesRef = useRef(0)
  const releaseRef = useRef<(() => void) | null>(null)
  // 供模块级存活表判断「这张画布是否还在视口内」，避免逐出用户正在看的图。
  const visibleRef = useRef(false)
  visibleRef.current = visible
  // lazyAfter/index 仅用于保持调用方签名（实际门控由 IntersectionObserver 的 ±3000px 带完成）。
  void lazyAfter
  void index

  const needDescramble = !isGif(src) && scrambleId !== '0'
  // 熔断后新进入的图片直接走服务端；没有兜底地址时只能继续尝试本机还原。
  const servedByServer = Boolean(serverSrc) && (useServer || canvasDisabled)

  // 视口门控：进入 ±3000px 带内才持有实体，离开则释放画布位图，回来再还原。
  useEffect(() => {
    const el = holderRef.current
    if (!el) {
      setVisible(true)
      return
    }
    return observeVisibility(el, setVisible)
  }, [])

  // 释放当前持有画布：回到占位态，等再次进入视口时重新还原。
  // 统一走 release()，这样「离开视口」这条最常见的路径也会在 DOM 摘除后立刻清空位图
  //（只靠 GC 回收不够及时，iOS 上尤其明显）。
  const releaseCurrent = useCallback(() => {
    const release = releaseRef.current
    if (release) {
      dropCanvas(release)
      releaseRef.current = null
      release()
      return
    }
    setCanvasEl(null)
    setDisplaySrc('')
    setPhase('idle')
  }, [])

  // 离开视口后释放位图，避免 canvas 位图随阅读进度无限累积。
  useEffect(() => {
    if (visible || phase !== 'done') return
    if (canvasEl) releaseCurrent()
  }, [visible, phase, canvasEl, releaseCurrent])

  const cutImage = useCallback(
    (image: HTMLImageElement, token: number): boolean => {
      try {
        const width = image.naturalWidth
        const height = image.naturalHeight
        const pictureName = src.substring(src.lastIndexOf('/') + 1).split('?')[0].split('.')[0]
        const sliceCount = getSegmentationNum(comicId, scrambleId, pictureName)
        // 该图本就不需要还原：直接用原图（不是失败，也不要占用 canvas）。
        if (!width || !height || sliceCount <= 1 || height < sliceCount * 2) {
          setDisplaySrc(src)
          setCanvasEl(null)
          return true
        }

        const canvas = document.createElement('canvas')
        canvas.width = width
        canvas.height = height
        const context = canvas.getContext('2d')
        // 拿不到 2D 上下文是 iOS canvas 内存耗尽的典型信号：本机还原不了，交给服务端。
        if (!context) return false

        // 与旧版一致：先构建各块的 [startY, endY]，再从最后一块向前依次绘制
        const rem = height % sliceCount
        const copyHeight = Math.floor(height / sliceCount)
        const blocks: Array<[number, number]> = []
        let totalH = 0
        for (let i = 0; i < sliceCount; i++) {
          let h = copyHeight * (i + 1)
          if (i === sliceCount - 1) {
            h += rem
          }
          blocks.push([totalH, h])
          totalH = h
        }

        let destY = 0
        for (let i = blocks.length - 1; i >= 0; i--) {
          const start = blocks[i][0]
          const end = blocks[i][1]
          const sliceH = end - start
          context.drawImage(image, 0, start, width, sliceH, 0, destY, width, sliceH)
          destY += sliceH
        }

        // 校验：canvas 被静默清空时像素会全为 0。先读中心 1 个像素——
        // 常见路径只有这一次读取；中心透明时再取对角两点确认，
        // 避免把「中心本身就有透明像素」的图误判成画布失效。
        const cx = Math.floor(width / 2)
        const cy = Math.floor(height / 2)
        if (!context.getImageData(cx, cy, 1, 1).data[3]) {
          const leftTop = context.getImageData(0, 0, 1, 1).data[3]
          const rightBottom = context.getImageData(width - 1, height - 1, 1, 1).data[3]
          if (!leftTop && !rightBottom) return false
        }

        if (loadTokenRef.current !== token) return false
        // 直接挂载 canvas 元素，跳过 toDataURL 重编码，实现无损显示（兼容所有浏览器）
        setCanvasEl(canvas)
        setDisplaySrc('')
        return true
      } catch {
        return false
      }
    },
    [src, comicId, scrambleId],
  )

  useEffect(() => {
    if (phase === 'idle' && visible) {
      setPhase('loading')
    }
  }, [phase, visible])

  useEffect(() => {
    if (phase !== 'loading') return
    const token = ++loadTokenRef.current

    const bust = (u: string) => (imgKey > 0 ? `${u}${u.includes('?') ? '&' : '?'}retry=${imgKey}` : u)
    const direct = !needDescramble || servedByServer
    const url = direct && servedByServer && serverSrc ? bust(serverSrc) : bust(src)
    const image = new Image()
    image.decoding = 'async'
    // 跨域图片（例如源站直链）必须带 CORS 才能把像素读回画布；
    // 不带的话 getImageData 会抛 SecurityError，被当成「本机还原失败」而误走服务端。
    if (/^https?:\/\//i.test(url) && !url.startsWith(window.location.origin)) {
      image.crossOrigin = 'anonymous'
    }

    const cleanup = () => {
      image.onload = null
      image.onerror = null
    }

    const retryOrFail = () => {
      if (retriesRef.current < 3) {
        retriesRef.current += 1
        setImgKey((k) => k + 1)
      } else {
        setPhase('error')
      }
    }

    image.onload = () => {
      cleanup()
      if (loadTokenRef.current !== token) return
      const w = image.naturalWidth || 0
      const h = image.naturalHeight || 0
      if (w > 0 && h > 0) {
        setNatW(w)
        setNatH(h)
      }
      // 普通展示（不需要还原，或已切到服务端兜底通道）
      if (direct) {
        setDisplaySrc(url)
        setCanvasEl(null)
        setPhase('done')
        return
      }
      if (cutImage(image, token)) {
        setPhase('done')
        return
      }
      if (loadTokenRef.current !== token) return
      // 本机还原失败：有兜底就切到服务端通道，没有就只能继续重试。
      noteCanvasFailure()
      if (serverSrc) {
        // 只翻 useServer，让下面的加载 effect 以 serverSrc 重新走一遍管线，
        // 从而复用统一的错误处理与重试（直接塞 <img> 的话失败就没救了）。
        setUseServer(true)
        return
      }
      retryOrFail()
    }
    image.onerror = () => {
      cleanup()
      if (loadTokenRef.current !== token) return
      retryOrFail()
    }
    image.src = url
    return cleanup
  }, [phase, visible, imgKey, src, serverSrc, needDescramble, servedByServer, cutImage])

  // 组件卸载或换页时使进行中的任务失效，并释放持有的画布。
  useEffect(() => {
    return () => {
      loadTokenRef.current += 1
      if (releaseRef.current) {
        dropCanvas(releaseRef.current)
        releaseRef.current = null
      }
    }
  }, [])

  // 将 descramble 后的 canvas 元素直接挂载到容器，无损显示
  useEffect(() => {
    const container = canvasContainerRef.current
    if (!container || !canvasEl) return
    container.innerHTML = ''
    canvasEl.style.width = '100%'
    canvasEl.style.margin = '0 auto'
    canvasEl.style.display = 'block'
    canvasEl.style.userSelect = 'none'
    canvasEl.style.webkitUserSelect = 'none'
    container.appendChild(canvasEl)
    // 登记到模块级存活表：超出张数/字节预算时回收离视口最远的画布。
    const release = () => {
      // 这里只回退到占位态，位图清空交给下面的 effect cleanup（DOM 摘除之后）做。
      // 顺序很关键：在「还挂在页面上」的 canvas 上直接把宽高设成 0，会让它当场塌成 0 高，
      // 在两帧之间留下一次画面突变——iOS 上这一帧特别容易被看到。
      discardedCanvases.add(canvasEl)
      setCanvasEl(null)
      setDisplaySrc('')
      setPhase('idle')
    }
    releaseRef.current = release
    retainCanvas({
      release,
      bytes: canvasEl.width * canvasEl.height * 4,
      active: () => visibleRef.current,
      distance: () => viewportDistance(holderRef.current),
    })
    return () => {
      dropCanvas(release)
      if (releaseRef.current === release) releaseRef.current = null
      if (canvasEl.parentElement === container) container.removeChild(canvasEl)
      // 从 DOM 上摘掉之后再置零位图，让 iOS 立刻回收 canvas 显存
      //（只等 GC 不够及时，而 iOS 对 canvas 总内存有硬上限，正是「图片未解码」的根因）。
      // 只有确实被丢弃过（走过 release）的才置零，避免 StrictMode 重挂时把活的位图清掉。
      if (discardedCanvases.has(canvasEl)) {
        discardedCanvases.delete(canvasEl)
        canvasEl.width = 0
        canvasEl.height = 0
      }
    }
  }, [canvasEl])

  // 宽度上限只改样式，不再重挂 canvas：重挂会把上一步刚清空的位图又贴回去（页面变空白）。
  useEffect(() => {
    if (!canvasEl) return
    canvasEl.style.maxWidth = fullWidth ? 'none' : '900px'
  }, [canvasEl, fullWidth])

  const retry = () => {
    retriesRef.current = 0
    setImgKey(0)
    setDisplaySrc('')
    setCanvasEl(null)
    setUseServer(false)
    setPhase('idle')
  }

  // 已解码/已加载才展示实体；否则用等高占位顶住布局
  const hasBox = natW > 0 && natH > 0
  const showContent = phase === 'done' && Boolean(canvasEl || displaySrc)

  return (
    <Box
      ref={holderRef}
      sx={{
        position: 'relative',
        // 必须给 holder 确定宽度：父容器是 flex + justify-content:center，若此处为 auto，
        // 内部 width:100% 的等比占位会因宽度未定而塌陷成 0 高，造成占位与实体切换时高度突变、滚动跳顶。
        width: '100%',
        maxWidth: fullWidth ? 'none' : 900,
        mx: 'auto',
        minHeight: showContent || hasBox ? undefined : '40vh',
      }}
    >
      {showContent ? (
        canvasEl ? (
          <Box
            ref={canvasContainerRef}
            sx={{ width: '100%', aspectRatio: hasBox ? `${natW} / ${natH}` : undefined, display: 'flex', justifyContent: 'center' }}
          />
        ) : (
          <Box
            component="img"
            src={displaySrc}
            alt=""
            decoding="async"
            sx={{ display: 'block', width: '100%', maxWidth: fullWidth ? 'none' : 900, mx: 'auto', userSelect: 'none', WebkitUserSelect: 'none' }}
          />
        )
      ) : hasBox ? (
        <Box
          sx={{
            width: '100%',
            aspectRatio: `${natW} / ${natH}`,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          {phase === 'error' ? (
            <Box
              onClick={retry}
              sx={{ py: 4, textAlign: 'center', color: 'text.secondary', cursor: 'pointer', '&:hover': { color: 'primary.main' } }}
            >
              <BrokenImageIcon sx={{ fontSize: 42, mb: 1 }} />
              <Typography variant="body2">图片加载失败，点击重试</Typography>
            </Box>
          ) : phase === 'loading' ? (
            <CircularProgress size={28} thickness={4} />
          ) : null}
        </Box>
      ) : phase === 'error' ? (
        <Box
          onClick={retry}
          sx={{
            py: 8,
            textAlign: 'center',
            color: 'text.secondary',
            cursor: 'pointer',
            '&:hover': { color: 'primary.main' },
          }}
        >
          <BrokenImageIcon sx={{ fontSize: 42, mb: 1 }} />
          <Typography variant="body2">图片加载失败，点击重试</Typography>
        </Box>
      ) : (
        <Box sx={{ py: 8, textAlign: 'center' }}>
          <CircularProgress size={28} thickness={4} />
          {needDescramble && !servedByServer && (
            <Typography variant="caption" color="text.secondary" display="block" mt={1}>
              解码中…
            </Typography>
          )}
        </Box>
      )}
    </Box>
  )
}

// 属性均为原始值，浅比较即可稳定跳过未变化页面的重渲染
export default memo(DscImage)
