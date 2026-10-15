// 沉浸式阅读器：对齐旧版 reader.html 的全部能力
// 真全屏 fixed 层 + 自动隐藏双栏 + 底部四键 dock + 选话/设置抽屉
// 渐进加载（初始/追加/回滚补偿）+ 阅读进度保存与恢复 + Safari/iOS 兼容
import * as React from 'react'
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import CircularProgress from '@mui/material/CircularProgress'
import Drawer from '@mui/material/Drawer'
import IconButton from '@mui/material/IconButton'
import Slider from '@mui/material/Slider'
import Typography from '@mui/material/Typography'
import ArrowBackIcon from '@mui/icons-material/ArrowBack'
import CheckCircleIcon from '@mui/icons-material/CheckCircle'
import FavoriteIcon from '@mui/icons-material/Favorite'
import FavoriteBorderIcon from '@mui/icons-material/FavoriteBorder'
import MenuBookIcon from '@mui/icons-material/MenuBook'
import NavigateBeforeIcon from '@mui/icons-material/NavigateBefore'
import NavigateNextIcon from '@mui/icons-material/NavigateNext'
import TuneIcon from '@mui/icons-material/Tune'
import { api, ApiError, UNAUTHORIZED_EVENT } from '../api'
import { FavoriteFolderDialog, useAsync } from '../components'
import type { FavoriteFolder } from '../components'
import { useAuth } from '../auth'
import { useToast } from '../toast'
import DscImage, {
  DEFAULT_KEEP_PAGES,
  notifyViewportChanged,
  resetCanvasAvailability,
  setKeepPages,
} from '../components/DscImage'
import { appLink } from '../source'
import { chapterImageServerUrl, chapterImageUrl } from '../types'
import type { ChapterDetail, ComicDetail } from '../types'

interface ReaderSettings {
  width: number
  gap: number
  initial: number
  batch: number
  /** 上方向保留页数（1..4）：越大越不容易「上一页黑掉」，代价是同时持有更多画布。 */
  keepPages: number
}

const DEFAULT_SETTINGS: ReaderSettings = { width: 100, gap: 0, initial: 4, batch: 3, keepPages: DEFAULT_KEEP_PAGES }
const SETTINGS_KEY = 'jm.reader.settings.v1'
// 旧版默认间距为 8px，且会被自动写入 localStorage；改默认 0px 后一并迁移存量数据
const LEGACY_GAP_DEFAULT = 8

function loadSettings(): ReaderSettings {
  try {
    const raw = localStorage.getItem(SETTINGS_KEY)
    if (!raw) return DEFAULT_SETTINGS
    const p = JSON.parse(raw) as Partial<ReaderSettings>
    const clamp = (v: unknown, min: number, max: number, fb: number) => {
      const n = Number(v)
      return Number.isFinite(n) ? Math.min(max, Math.max(min, n)) : fb
    }
    const gap =
      Number(p.gap) === LEGACY_GAP_DEFAULT
        ? DEFAULT_SETTINGS.gap
        : clamp(p.gap, 0, 50, DEFAULT_SETTINGS.gap)
    return {
      width: clamp(p.width, 20, 100, DEFAULT_SETTINGS.width),
      gap,
      initial: clamp(p.initial, 1, 12, DEFAULT_SETTINGS.initial),
      batch: clamp(p.batch, 1, 6, DEFAULT_SETTINGS.batch),
      keepPages: clamp(p.keepPages, 1, 4, DEFAULT_SETTINGS.keepPages),
    }
  } catch {
    return DEFAULT_SETTINGS
  }
}

function SettingSlider({
  label,
  unit,
  min,
  max,
  step,
  value,
  onChange,
}: {
  label: string
  unit: string
  min: number
  max: number
  step: number
  value: number
  onChange: (v: number) => void
}) {
  return (
    <Box>
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 1 }}>
        <Typography variant="body2" fontWeight={700} color="text.secondary">
          {label}
        </Typography>
        <Typography
          variant="caption"
          fontWeight={700}
          sx={{
            px: 1.5,
            py: 0.5,
            borderRadius: 999,
            bgcolor: (t) => (t.palette.mode === 'dark' ? 'rgba(144,202,249,0.12)' : 'rgba(2,136,209,0.08)'),
            color: 'primary.main',
          }}
        >
          {value}
          {unit}
        </Typography>
      </Box>
      <Slider
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(_, v) => onChange(Array.isArray(v) ? (v[0] ?? value) : v)}
        sx={{ touchAction: 'none' }}
      />
    </Box>
  )
}

export default function Reader() {
  const { chapterId = '' } = useParams()
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const { user } = useAuth()
  const { toast } = useToast()

  // 换章复位前端还原的熔断状态：上一章的偶发失败不该影响这一章。
  useEffect(() => {
    resetCanvasAvailability()
  }, [chapterId])

  const [settings, setSettings] = useState<ReaderSettings>(loadSettings)
  const [sheet, setSheet] = useState<null | 'chapters' | 'settings'>(null)
  const [sheetTab, setSheetTab] = useState<'chapters' | 'settings'>('chapters')
  const [controls, setControls] = useState(true)
  const [favBusy, setFavBusy] = useState(false)
  const [isFav, setIsFav] = useState(false)
  const [folderOpen, setFolderOpen] = useState(false)
  const [folderSaving, setFolderSaving] = useState(false)

  const [start, setStart] = useState(0)
  const [limit, setLimit] = useState(DEFAULT_SETTINGS.initial)

  const scrollRef = useRef<HTMLDivElement | null>(null)
  const hideTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const lastTop = useRef(0)
  const touchY = useRef(0)
  const scrollRaf = useRef(0)
  const prependRef = useRef<{ h: number; st: number } | null>(null)
  const saveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const pageIdx = useRef(0)
  // 用户是否手动改过设置：决定远端设置套不套用、改动要不要回写服务端。
  const touchedRef = useRef(false)

  const resumePage = useMemo(() => {
    const n = Number(params.get('page') ?? 0)
    return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
  }, [params])

  const state = useAsyncChapter(chapterId)
  const d = state.data
  const albumId = d?.raw?.album_id ?? ''
  const album = useAsyncAlbum(albumId)
  useEffect(() => {
    if (album.data) setIsFav(Boolean(album.data.is_favorite))
  }, [album.data])
  const chapters = album.data?.chapters ?? []
  const chIdx = chapters.findIndex((c) => c.id === chapterId)
  const prevCh = chIdx > 0 ? chapters[chIdx - 1] : null
  const nextCh = chIdx >= 0 && chIdx < chapters.length - 1 ? chapters[chIdx + 1] : null
  // 页面清单：bika 之类源直接给出图片 URL，JM 则回落到 chapterImageUrl 现拼。
  const pages = useMemo(
    () =>
      d
        ? d.images
            .map((im) => ({ name: im.name ?? '', url: im.url ?? '' }))
            .filter((p) => p.name !== '')
        : [],
    [d],
  )
  const names = useMemo(() => pages.map((p) => p.name), [pages])
  const scrambleId = String(d?.raw?.scramble_id ?? '0')
  const imgDomain = d?.raw?.data_original_domain ?? null
  const albumTitle = album.data?.title || d?.raw?.title || ''

  // 页面列表记忆化：控制栏/抽屉等状态变化时不重渲染整页图片子树
  const pageNodes = useMemo(
    () =>
      pages.map((page, i) =>
        i >= start && i < limit ? (
          <Box
            key={`${chapterId}-${page.name}-${i}`}
            data-page-index={i}
            sx={{ width: '100%', display: 'flex', justifyContent: 'center', bgcolor: '#000', lineHeight: 0, fontSize: 0 }}
          >
            <DscImage
              src={page.url || chapterImageUrl(chapterId, page.name, imgDomain)}
              serverSrc={page.url ? undefined : chapterImageServerUrl(chapterId, page.name, imgDomain, scrambleId)}
              comicId={chapterId}
              scrambleId={scrambleId}
              index={i}
              lazyAfter={settings.initial}
              fullWidth
            />
          </Box>
        ) : null,
      ),
    [names, start, limit, chapterId, imgDomain, scrambleId],
  )

  // 所有设置改动都走这里：标记「用户动过」，供远端套用/回写判断。
  const applySetting = (patch: Partial<ReaderSettings>) => {
    touchedRef.current = true
    setSettings((s) => ({ ...s, ...patch }))
  }

  const showControls = () => {
    setControls(true)
    if (hideTimer.current) clearTimeout(hideTimer.current)
    hideTimer.current = setTimeout(() => setControls(false), 2200)
  }
  const hideControls = () => {
    setControls(false)
    if (hideTimer.current) clearTimeout(hideTimer.current)
  }

  // 切话：重置渲染窗口、回到顶部、唤起控制栏
  useEffect(() => {
    setStart(0)
    setLimit(settings.initial)
    pageIdx.current = 0
    lastTop.current = 0
    scrollRef.current?.scrollTo({ top: 0 })
    showControls()
    return () => {
      if (hideTimer.current) clearTimeout(hideTimer.current)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chapterId])

  // 章节数据到达：按续读页计算初始窗口 + 记录历史
  useEffect(() => {
    if (!d) return
    const total = d.images.length
    const ini = settings.initial
    const s = resumePage > 1 ? Math.max(0, resumePage - 1) : 0
    const end =
      resumePage > 0
        ? Math.min(total, Math.max(resumePage + Math.max(2, ini), s + ini))
        : Math.min(total, ini)
    setStart(s)
    setLimit(total > 0 ? Math.max(s + 1, end) : ini)
    if (user && albumId) {
      void api
        .post('/api/aura/library/history', {
          album_id: albumId,
          album_title: albumTitle,
          photo_id: chapterId,
          title: d.title ?? '',
          page_index: resumePage,
          type: 'comic',
          timestamp: Date.now(),
        })
        .catch(() => {})
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [d, chapterId])

  // 续读定位：等待目标页及其前方页面解码完成后滚动（解码中占位 40vh 会使 offsetTop 失真）
  useEffect(() => {
    if (!d || resumePage <= 0) return
    let tries = 0
    let raf = 0
    const tick = () => {
      tries += 1
      const root = scrollRef.current
      const node = root?.querySelector<HTMLElement>(`[data-page-index="${resumePage}"]`)
      if (root && node) {
        // 渲染窗口自 start 起，仅需等待窗口内、目标页之前的页面就绪
        const from = resumePage > 1 ? resumePage - 1 : 0
        let pending = false
        for (let i = from; i < resumePage; i++) {
          if (!root.querySelector(`[data-page-index="${i}"] canvas, [data-page-index="${i}"] img`)) {
            pending = true
            break
          }
        }
        // 前方页面全部解码完成后再定位；超时兜底防止单图加载失败卡死续读
        if (!pending || tries > 900) {
          root.scrollTo({ top: Math.max(0, node.offsetTop - 12), behavior: 'auto' })
          return
        }
      }
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [d, resumePage])

  // 设置持久化
  useEffect(() => {
    try {
      localStorage.setItem(SETTINGS_KEY, JSON.stringify(settings))
    } catch {
      /* 隐私模式写入失败可忽略 */
    }
  }, [settings])

  // 保留页数是 DscImage 的模块级共享状态，设置一变必须立刻同步，
  // 否则改完设置要等下次挂载才生效。
  useEffect(() => {
    setKeepPages(settings.keepPages)
  }, [settings.keepPages])

  // 阅读器设置随账号存到服务端：localStorage 只活在单台设备上，
  // 换设备/清缓存就丢，而「保留几页」直接关系内存与卡顿，值得跟账号走。
  // 访客没有账号，继续只用 localStorage。
  useEffect(() => {
    if (!user) return
    let cancelled = false
    void (async () => {
      try {
        const remote = await api.get<Record<string, unknown> | null>('/api/aura/reader/settings')
        if (cancelled || !remote) return
        // 只在用户还没动过设置时套用远端值，避免刚打开就覆盖用户刚拖的滑块。
        if (touchedRef.current) return
        setSettings((s) => {
          const num = (v: unknown, min: number, max: number, fb: number) => {
            const n = Number(v)
            return Number.isFinite(n) ? Math.min(max, Math.max(min, n)) : fb
          }
          return {
            width: num(remote.width, 20, 100, s.width),
            gap: num(remote.gap, 0, 50, s.gap),
            initial: num(remote.initial, 1, 12, s.initial),
            batch: num(remote.batch, 1, 6, s.batch),
            keepPages: num(remote.keep_pages, 1, 4, s.keepPages),
          }
        })
      } catch {
        /* 取不到就用本地值，不打扰阅读 */
      }
    })()
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user?.username])

  // 改动防抖回写服务端（拖滑块会连续触发，别每帧打一次接口）。
  useEffect(() => {
    if (!user || !touchedRef.current) return
    const t = setTimeout(() => {
      void api
        .put('/api/aura/reader/settings', {
          width: settings.width,
          gap: settings.gap,
          initial: settings.initial,
          batch: settings.batch,
          keep_pages: settings.keepPages,
        })
        .catch(() => {})
    }, 800)
    return () => clearTimeout(t)
  }, [settings, user])

  // 卸载时兜底上报最后进度
  useEffect(() => {
    return () => {
      if (saveTimer.current) clearTimeout(saveTimer.current)
      if (scrollRaf.current) cancelAnimationFrame(scrollRaf.current)
      scrollRaf.current = 0
      if (user && albumId) {
        void api
          .post('/api/aura/library/history', {
            album_id: albumId,
            photo_id: chapterId,
            page_index: pageIdx.current,
            type: 'comic',
          })
          .catch(() => {})
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chapterId])

  const saveProgress = (root: HTMLDivElement) => {
    if (!d) return
    if (saveTimer.current) clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => {
      saveTimer.current = null
      const nodes = root.querySelectorAll<HTMLElement>('[data-page-index]')
      if (!nodes.length) return
      const threshold = root.scrollTop + root.clientHeight * 0.35
      let cur = 0
      for (const n of nodes) {
        const idx = Number(n.dataset.pageIndex ?? 0)
        if (n.offsetTop <= threshold) cur = idx
        else break
      }
      pageIdx.current = cur
      if (user && albumId) {
        void api
          .post('/api/aura/library/history', {
            album_id: albumId,
            album_title: albumTitle,
            photo_id: chapterId,
            title: d.title ?? '',
            page_index: cur,
            type: 'comic',
          })
          .catch(() => {})
      }
    }, 500)
  }

  const maybeLoadMore = (root: HTMLDivElement) => {
    if (!d) return
    const total = d.images.length
    if (total <= 0) return
    const sh = root.scrollHeight
    const ch = root.clientHeight
    const st = root.scrollTop
    if (!sh || !ch) return
    const batch = Math.max(1, settings.batch)
    if (st < 800 && start > 0) {
      const nextStart = Math.max(0, start - batch)
      if (nextStart < start) {
        prependRef.current = { h: sh, st }
        setStart(nextStart)
      }
      return
    }
    const cur = Math.max(start, limit)
    if (cur >= total) return
    if (sh - (st + ch) > 1200) return
    setLimit(Math.min(total, cur + batch))
  }

  // 向前补页后保持视觉位置不跳动
  useLayoutEffect(() => {
    const p = prependRef.current
    const root = scrollRef.current
    if (p && root) {
      prependRef.current = null
      const delta = root.scrollHeight - p.h
      if (delta > 0) root.scrollTop = p.st + delta
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [start])

  const onScroll = (e: React.UIEvent<HTMLDivElement>) => {
    const root = e.currentTarget
    if (root.scrollTop < lastTop.current - 8) hideControls()
    lastTop.current = root.scrollTop
    // 滚动高频触发：布局测量与追加加载按帧节流，避免每次事件都强制重排
    if (!scrollRaf.current) {
      scrollRaf.current = requestAnimationFrame(() => {
        scrollRaf.current = 0
        const el = scrollRef.current
        if (el) maybeLoadMore(el)
        // 保留范围随滚动连续变化，这里统一让所有已挂载图片重算一次
        //（载入/释放都走同一个判定，见 DscImage 的 keepElement）。
        notifyViewportChanged()
      })
    }
    saveProgress(root)
  }

  const onCanvasClick = () => {
    if (sheet) {
      setSheet(null)
      return
    }
    if (controls) {
      hideControls()
      return
    }
    showControls()
  }

  // 章内切话用 replace：阅读页始终只占用一个历史位。
  // 否则每切一次话都会压深一层，「详情 → 阅读 → 下一话」后点返回
  // 会先退回上一话，而不是直接回到详情页。
  const jump = (cid: string | null | undefined) => {
    if (!cid) return
    navigate(appLink(`/reader/${encodeURIComponent(cid)}`), { replace: true })
  }

  // 退出阅读：优先「退栈」回上一页。
  // 从详情页进入时即回到原来那条详情页记录（不新增历史），
  // 避免再 push 一条详情页造成「详情 → 阅读 → 详情」的返回死循环；
  // 直接打开阅读页（无站内历史可退）时，用 replace 兜底跳详情，同样不新增记录。
  const exitReader = () => {
    if ((window.history.state?.idx ?? 0) > 0) {
      navigate(-1)
      return
    }
    navigate(appLink(albumId ? `/comic/${encodeURIComponent(albumId)}` : '/'), { replace: true })
  }

  const toggleFav = async () => {
    if (!albumId || favBusy) return
    const desired = !isFav
    setFavBusy(true)
    try {
      const res = await api.post<{
        is_favorite?: boolean
        result?: { type?: string; status?: string; msg?: string }
      }>(`/api/v2/jm/comic/${encodeURIComponent(albumId)}/favorite`, { desired_state: desired })
      const next = typeof res?.is_favorite === 'boolean' ? res.is_favorite : desired
      setIsFav(next)
      toast(next ? '已收藏' : '已取消收藏')
      // 新增收藏成功后，按详情页一致的口径弹出收藏夹选择
      const opType = String(res?.result?.type ?? '').toLowerCase()
      if (next && (opType === 'add' || opType === 'edit' || opType === 'move')) setFolderOpen(true)
    } catch (e) {
      if (e instanceof ApiError && e.st === 1014) window.dispatchEvent(new Event(UNAUTHORIZED_EVENT))
      else toast(e instanceof ApiError ? e.message : '操作失败', 'error')
    } finally {
      setFavBusy(false)
    }
  }

  const loadFolders = async (): Promise<FavoriteFolder[]> => {
    const d = await api.get<{ folders?: { id: string; name: string }[] }>(
      `/api/favorites${api.qs({ page: 1, folder_id: '0' })}`,
    )
    return (d?.folders ?? []).map((f) => ({ id: String(f.id ?? ''), name: String(f.name ?? '') }))
  }

  const applyFolder = async (choice: { kind: 'folder'; folderId: string } | { kind: 'new'; folderName: string }) => {
    if (!albumId) return
    if (choice.kind === 'folder' && (!choice.folderId || choice.folderId === '0')) {
      setFolderOpen(false)
      return
    }
    setFolderSaving(true)
    try {
      const payload =
        choice.kind === 'new'
          ? { type: 'add', folder_name: choice.folderName, album_id: albumId }
          : { type: 'move', folder_id: choice.folderId, album_id: albumId }
      await api.post('/api/favorite_folder', payload)
      toast(choice.kind === 'new' ? '已新建并加入收藏夹' : '已移动到该收藏夹')
      setFolderOpen(false)
    } catch (e) {
      if (e instanceof ApiError && e.st === 1014) window.dispatchEvent(new Event(UNAUTHORIZED_EVENT))
      else toast(e instanceof ApiError ? e.message : '收藏夹操作失败', 'error')
    } finally {
      setFolderSaving(false)
    }
  }

  const openSheet = (tab: 'chapters' | 'settings') => {
    setSheetTab(tab)
    setSheet(tab)
    showControls()
  }

  const barsHidden = !controls
  const count = chapters.length

  return (
    <Box
      ref={scrollRef}
      onScroll={onScroll}
      onClick={onCanvasClick}
      onTouchStart={(e) => {
        touchY.current = e.touches[0]?.clientY ?? 0
      }}
      onTouchEnd={(e) => {
        const endY = e.changedTouches[0]?.clientY ?? 0
        if (touchY.current - endY > 40) hideControls()
      }}
      sx={{
        position: 'fixed',
        inset: 0,
        zIndex: 1200,
        bgcolor: '#000',
        overflowY: 'auto',
        overscrollBehavior: 'contain',
        WebkitOverflowScrolling: 'touch',
        touchAction: 'pan-y',
        WebkitTapHighlightColor: 'transparent',
        cursor: 'pointer',
      }}
    >
      {/* 顶栏（自动隐藏） */}
      <Box
        sx={{
          position: 'fixed',
          top: 0,
          left: 0,
          right: 0,
          zIndex: 1220,
          pointerEvents: 'none',
          paddingTop: 'env(safe-area-inset-top)',
          background: 'linear-gradient(to bottom, rgba(0,0,0,0.82), rgba(0,0,0,0.42) 60%, transparent)',
          transition: 'transform .3s ease, opacity .3s ease',
          transform: barsHidden ? 'translateY(-110%)' : 'none',
          opacity: barsHidden ? 0 : 1,
          visibility: barsHidden ? 'hidden' : 'visible',
        }}
      >
        <Box
          onClick={(e) => e.stopPropagation()}
          sx={{ pointerEvents: controls ? 'auto' : 'none', display: 'flex', alignItems: 'center', gap: 1.5, p: 1.5 }}
        >
          <IconButton
            aria-label="退出阅读"
            onClick={exitReader}
            sx={{
              bgcolor: 'rgba(255,255,255,0.12)',
              color: '#fff',
              backdropFilter: 'blur(12px)',
              WebkitBackdropFilter: 'blur(12px)',
              '&:hover': { bgcolor: 'rgba(255,255,255,0.2)' },
            }}
          >
            <ArrowBackIcon />
          </IconButton>
          <Box
            sx={{
              flex: 1,
              minWidth: 0,
              display: 'flex',
              alignItems: 'center',
              gap: 1.25,
              px: 2,
              py: 1,
              borderRadius: 999,
              bgcolor: 'rgba(0,0,0,0.4)',
              backdropFilter: 'blur(12px)',
              WebkitBackdropFilter: 'blur(12px)',
              border: '1px solid rgba(255,255,255,0.06)',
            }}
          >
            <MenuBookIcon sx={{ color: '#90caf9', fontSize: 20 }} />
            <Box sx={{ minWidth: 0, flex: 1 }}>
              <Typography noWrap sx={{ color: '#fff', fontSize: 13, fontWeight: 600, lineHeight: 1.3 }}>
                {d?.title || `#${chapterId}`}
              </Typography>
              <Typography noWrap sx={{ color: 'rgba(255,255,255,0.65)', fontSize: 11, lineHeight: 1.4 }}>
                {albumId ? `ID: ${albumId}` : ''}
                {count > 0 && chIdx >= 0 ? ` · ${chIdx + 1} / ${count}` : ''}
              </Typography>
            </Box>
          </Box>
          <IconButton
            aria-label="阅读设置"
            onClick={() => openSheet('settings')}
            sx={{
              bgcolor: sheet === 'settings' ? 'primary.main' : 'rgba(255,255,255,0.12)',
              color: '#fff',
              backdropFilter: 'blur(12px)',
              WebkitBackdropFilter: 'blur(12px)',
              '&:hover': { bgcolor: 'rgba(255,255,255,0.2)' },
            }}
          >
            <TuneIcon />
          </IconButton>
        </Box>
      </Box>

      {/* 底部 dock（自动隐藏）：上一话 / 选话 / 收藏 / 下一话 */}
      <Box
        sx={{
          position: 'fixed',
          bottom: 0,
          left: 0,
          right: 0,
          zIndex: 1220,
          display: 'flex',
          justifyContent: 'center',
          pointerEvents: 'none',
          paddingBottom: 'max(env(safe-area-inset-bottom), 14px)',
          transition: 'transform .3s ease, opacity .3s ease',
          transform: barsHidden ? 'translateY(130%)' : 'none',
          opacity: barsHidden ? 0 : 1,
          visibility: barsHidden ? 'hidden' : 'visible',
        }}
      >
        <Box
          onClick={(e) => e.stopPropagation()}
          sx={{
            pointerEvents: controls ? 'auto' : 'none',
            width: '92%',
            maxWidth: 520,
            borderRadius: '26px',
            bgcolor: 'rgba(12,12,14,0.72)',
            backdropFilter: 'blur(20px)',
            WebkitBackdropFilter: 'blur(20px)',
            border: '1px solid rgba(255,255,255,0.08)',
            boxShadow: '0 12px 40px rgba(0,0,0,0.5)',
            overflow: 'hidden',
          }}
        >
          <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 1, px: 1.5, pt: 1.5 }}>
            <DockButton icon={<NavigateBeforeIcon fontSize="small" />} label="上一话" disabled={!prevCh} onClick={() => jump(prevCh?.id)} />
            <DockButton
              icon={<MenuBookIcon fontSize="small" />}
              label="选话"
              disabled={count === 0}
              onClick={() => openSheet('chapters')}
            />
            <DockButton
              icon={isFav ? <FavoriteIcon fontSize="small" /> : <FavoriteBorderIcon fontSize="small" />}
              label={isFav ? '已收藏' : '收藏'}
              disabled={!albumId || favBusy}
              onClick={() => void toggleFav()}
            />
            <DockButton icon={<NavigateNextIcon fontSize="small" />} label="下一话" disabled={!nextCh} onClick={() => jump(nextCh?.id)} trailing />
          </Box>
          <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 2, px: 2.5, pt: 0.75, pb: 1.5 }}>
            <Typography noWrap sx={{ color: 'rgba(255,255,255,0.55)', fontSize: 11, maxWidth: '60%' }}>
              {albumTitle}
            </Typography>
            {count > 0 && chIdx >= 0 && (
              <Typography sx={{ color: 'rgba(255,255,255,0.55)', fontSize: 11, flexShrink: 0 }}>
                {chIdx + 1} / {count}
              </Typography>
            )}
          </Box>
        </Box>
      </Box>

      {/* 画布区 */}
      <Box
        sx={{
          position: 'relative',
          zIndex: 0,
          mx: 'auto',
          width: `${settings.width}%`,
          maxWidth: `${settings.width}%`,
          minHeight: '100%',
          bgcolor: '#000',
          overflowX: 'hidden',
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          pb: '150px',
          transition: 'width .3s ease',
        }}
      >
        {state.loading ? (
          <Box sx={{ minHeight: '70vh', display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 2 }}>
            <CircularProgress size={36} sx={{ color: 'rgba(255,255,255,0.85)' }} />
            <Typography sx={{ color: 'rgba(255,255,255,0.55)', fontSize: 13 }}>正在加载章节…</Typography>
          </Box>
        ) : state.error || !d ? (
          <Box sx={{ minHeight: '70vh', display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 2, px: 4, textAlign: 'center' }}>
            <Typography sx={{ color: 'rgba(255,255,255,0.7)' }}>{state.error ?? '章节不存在'}</Typography>
            <Button variant="contained" onClick={state.reload}>
              重试
            </Button>
            {albumId && (
              <Button onClick={exitReader} sx={{ color: 'rgba(255,255,255,0.6)' }}>
                返回详情
              </Button>
            )}
          </Box>
        ) : names.length === 0 ? (
          <Box sx={{ minHeight: '70vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
            <Typography sx={{ color: 'rgba(255,255,255,0.5)' }}>本话没有可用图片</Typography>
          </Box>
        ) : (
          <>
            <Box sx={{ display: 'flex', flexDirection: 'column', width: '100%', alignItems: 'center', gap: `${settings.gap}px` }}>
              {pageNodes}
            </Box>
            <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 3, py: 10 }}>
              <Box sx={{ width: 80, height: 4, borderRadius: 999, bgcolor: 'rgba(255,255,255,0.12)' }} />
              <CheckCircleIcon sx={{ fontSize: 48, color: '#66bb6a' }} />
              <Typography sx={{ color: 'rgba(255,255,255,0.6)', fontSize: 16 }}>已读完本话</Typography>
              {albumId && (
                <Button
                  variant="contained"
                  onClick={(e) => {
                    e.stopPropagation()
                    exitReader()
                  }}
                  sx={{ borderRadius: 999, px: 4, py: 1.2, fontWeight: 700 }}
                >
                  返回详情
                </Button>
              )}
            </Box>
          </>
        )}
      </Box>

      {/* 选话 / 设置 抽屉 */}
      <Drawer
        anchor="bottom"
        open={sheet !== null}
        onClose={() => setSheet(null)}
        slotProps={{
          paper: {
            sx: {
              bgcolor: 'background.paper',
              borderTopLeftRadius: 24,
              borderTopRightRadius: 24,
              maxHeight: '76vh',
              paddingBottom: 'max(env(safe-area-inset-bottom), 14px)',
            },
          },
        }}
      >
        <Box onClick={(e) => e.stopPropagation()} sx={{ px: 2.5, pt: 2.5 }}>
          <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 2, pb: 2 }}>
            <Box sx={{ minWidth: 0 }}>
              <Typography fontWeight={700} noWrap>
                {d?.title || albumTitle || 'Reader'}
              </Typography>
              {count > 0 && chIdx >= 0 && (
                <Typography variant="caption" color="text.secondary">
                  {chIdx + 1} / {count}
                </Typography>
              )}
            </Box>
            <IconButton onClick={() => setSheet(null)} aria-label="关闭" sx={{ bgcolor: 'action.hover' }}>
              ✕
            </IconButton>
          </Box>

          <Box sx={{ display: 'flex', p: 0.5, borderRadius: 999, bgcolor: 'action.hover', mb: 2 }}>
            {(['chapters', 'settings'] as const).map((t) => (
              <Box
                key={t}
                component="button"
                onClick={() => setSheetTab(t)}
                sx={{
                  flex: 1,
                  height: 38,
                  border: 'none',
                  cursor: 'pointer',
                  borderRadius: 999,
                  fontWeight: 700,
                  fontSize: 13,
                  transition: 'all .2s ease',
                  ...(sheetTab === t
                    ? { bgcolor: 'primary.main', color: 'primary.contrastText' }
                    : { bgcolor: 'transparent', color: 'text.secondary' }),
                }}
              >
                {t === 'chapters' ? '选话' : '设置'}
              </Box>
            ))}
          </Box>

          {sheetTab === 'chapters' ? (
            <Box sx={{ maxHeight: '56vh', overflowY: 'auto', pb: 2, WebkitOverflowScrolling: 'touch' }}>
              {chapters.map((c) => {
                const current = c.id === chapterId
                return (
                  <Box
                    key={c.id}
                    component="button"
                    onClick={() => {
                      setSheet(null)
                      jump(c.id)
                    }}
                    sx={{
                      display: 'flex',
                      width: '100%',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      gap: 2,
                      textAlign: 'left',
                      border: 'none',
                      cursor: 'pointer',
                      p: 1.75,
                      mb: 1,
                      borderRadius: 3,
                      bgcolor: current ? 'rgba(144,202,249,0.12)' : 'action.hover',
                    }}
                  >
                    <Box sx={{ minWidth: 0 }}>
                      <Typography fontWeight={700} noWrap sx={{ fontSize: 14 }}>
                        {c.title || `Chapter ${c.id}`}
                      </Typography>
                      {current && (
                        <Typography variant="caption" color="primary" fontWeight={700}>
                          当前阅读
                        </Typography>
                      )}
                    </Box>
                    <NavigateNextIcon fontSize="small" sx={{ color: 'text.secondary', flexShrink: 0 }} />
                  </Box>
                )
              })}
            </Box>
          ) : (
            <Box sx={{ pb: 3, display: 'flex', flexDirection: 'column', gap: 3.5 }}>
              <SettingSlider
                label="图片宽度"
                unit="%"
                min={20}
                max={100}
                step={5}
                value={settings.width}
                onChange={(v) => applySetting({ width: v })}
              />
              <SettingSlider
                label="页面间距"
                unit="px"
                min={0}
                max={50}
                step={5}
                value={settings.gap}
                onChange={(v) => applySetting({ gap: v })}
              />
              <SettingSlider
                label="初始加载"
                unit="页"
                min={1}
                max={12}
                step={1}
                value={settings.initial}
                onChange={(v) => applySetting({ initial: v })}
              />
              <SettingSlider
                label="追加批量"
                unit="页"
                min={1}
                max={6}
                step={1}
                value={settings.batch}
                onChange={(v) => applySetting({ batch: v })}
              />
              <SettingSlider
                label="保留上几页"
                unit="页"
                min={1}
                max={4}
                step={1}
                value={settings.keepPages}
                onChange={(v) => applySetting({ keepPages: v })}
              />
              <Typography variant="caption" color="text.secondary" sx={{ mt: -2 }}>
                往上翻回去时不重新解码的页数：调大更不容易看到黑页，代价是同时占用更多内存。
              </Typography>
            </Box>
          )}
        </Box>
      </Drawer>

      <FavoriteFolderDialog
        open={folderOpen}
        loadFolders={loadFolders}
        submitting={folderSaving}
        onClose={() => setFolderOpen(false)}
        onConfirm={(choice) => void applyFolder(choice)}
      />
    </Box>
  )
}

function DockButton({
  icon,
  label,
  onClick,
  disabled,
  trailing,
}: {
  icon: React.ReactNode
  label: string
  onClick: () => void
  disabled?: boolean
  trailing?: boolean
}) {
  return (
    <Box
      component="button"
      onClick={onClick}
      disabled={disabled}
      sx={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        gap: 0.5,
        height: 46,
        border: 'none',
        cursor: disabled ? 'not-allowed' : 'pointer',
        borderRadius: '14px',
        bgcolor: 'rgba(255,255,255,0.06)',
        color: '#fff',
        fontSize: 12,
        fontWeight: 600,
        opacity: disabled ? 0.4 : 1,
        transition: 'background .2s ease, transform .1s ease',
        '&:active': { transform: 'scale(0.95)' },
        '&:hover': { bgcolor: 'rgba(255,255,255,0.12)' },
      }}
    >
      {!trailing && icon}
      {label}
      {trailing && icon}
    </Box>
  )
}

// 章节数据加载（保留旧数据，切话时平滑过渡）
function useAsyncChapter(chapterId: string) {
  return useAsync<ChapterDetail>(
    () => api.get<ChapterDetail>(`/api/v2/jm/chapter/${encodeURIComponent(chapterId)}`),
    [chapterId],
  )
}

function useAsyncAlbum(albumId: string) {
  return useAsync<ComicDetail | null>(
    () => (albumId ? api.get<ComicDetail>(`/api/v2/jm/comic/${encodeURIComponent(albumId)}`) : Promise.resolve(null)),
    [albumId],
  )
}
