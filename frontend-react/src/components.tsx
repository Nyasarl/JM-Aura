import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode, RefObject } from 'react'
import { Link as RouterLink } from 'react-router-dom'
import { appLink } from './source'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CardActionArea from '@mui/material/CardActionArea'
import CardMedia from '@mui/material/CardMedia'
import CircularProgress from '@mui/material/CircularProgress'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogContent from '@mui/material/DialogContent'
import DialogTitle from '@mui/material/DialogTitle'
import FormControlLabel from '@mui/material/FormControlLabel'
import Radio from '@mui/material/Radio'
import RadioGroup from '@mui/material/RadioGroup'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import { api, imgUrl } from './api'
import type { ComicSummary, NovelSummary } from './types'

/** 异步数据加载 Hook：自动执行、可重载、卸载安全。 */
export function useAsync<T>(loader: () => Promise<T>, deps: unknown[] = []): {
  loading: boolean
  error: string | null
  data: T | null
  reload: () => void
} {
  const [state, setState] = useState<{
    loading: boolean
    error: string | null
    data: T | null
  }>({ loading: true, error: null, data: null })
  const [tick, setTick] = useState(0)
  const loaderRef = useRef(loader)
  loaderRef.current = loader

  useEffect(() => {
    let alive = true
    setState((s) => ({ ...s, loading: true, error: null }))
    loaderRef.current().then(
      (data) => {
        if (alive) setState({ loading: false, error: null, data })
      },
      (err: unknown) => {
        if (alive)
          setState({
            loading: false,
            error: err instanceof Error ? err.message : String(err),
            data: null,
          })
      },
    )
    return () => {
      alive = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick])

  const reload = useCallback(() => setTick((t) => t + 1), [])
  return { ...state, reload }
}

/**
 * 页脚「当前在线人数」徽标。
 *
 * 极低频轮询（60s 一次）、标签页隐藏时暂停，且状态只存在于本组件内部，
 * 更新时不会触发父级重渲染——把占用压到最低。
 */
export function OnlineBadge() {
  const [count, setCount] = useState<number | null>(null)

  useEffect(() => {
    let alive = true
    let timer: number | undefined
    const load = () => {
      api
        .get<{ online: number }>('/api/site/online')
        .then((d) => {
          if (alive) setCount(d.online)
        })
        .catch(() => {
          /* 统计失败静默忽略，绝不影响页面本身 */
        })
    }
    const start = () => {
      load()
      timer = window.setInterval(load, 60000)
    }
    const stop = () => {
      if (timer !== undefined) {
        window.clearInterval(timer)
        timer = undefined
      }
    }
    const onVisibility = () => {
      if (document.hidden) stop()
      else if (timer === undefined) start()
    }
    if (!document.hidden) start()
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      alive = false
      stop()
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [])

  if (count === null) return null
  return (
    <Box sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.75 }}>
      <Box sx={{ width: 7, height: 7, borderRadius: '50%', bgcolor: 'success.main' }} />
      <Typography variant="caption" color="text.secondary">
        当前 {count} 人在线
      </Typography>
    </Box>
  )
}

const FALLBACK_REGISTER_URL = 'https://18comic.ink/signup'

/**
 * 官网注册地址（禁漫）：由后端 /capabilities 下发，域名轮换时无需重编前端；
 * 取不到时用兜底地址。登录页与注册页共用，避免两处各写一份。
 */
export function useRegisterTarget(): { url: string; host: string } {
  const caps = useAsync(() => api.get<{ register_url?: string }>('/api/v2/jm/capabilities'), [])
  const url = caps.data?.register_url || FALLBACK_REGISTER_URL
  let host = ''
  try {
    host = new URL(url).host
  } catch {
    /* URL 非法时不展示域名 */
  }
  return { url, host }
}

export function CenterLoading({ label }: { label?: string }) {
  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 2, py: 10 }}>
      <CircularProgress size={36} thickness={4} />
      {label && (
        <Typography variant="body2" color="text.secondary">
          {label}
        </Typography>
      )}
    </Box>
  )
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <Alert severity="error" action={
        onRetry && (
          <Button color="inherit" size="small" onClick={onRetry}>
            重试
          </Button>
        )
      }
    >
      {message}
    </Alert>
  )
}

export function EmptyState({ icon, text, action }: { icon?: ReactNode; text: string; action?: ReactNode }) {
  return (
    <Box sx={{ textAlign: 'center', py: 10, px: 2 }}>
      {icon && <Box sx={{ mb: 1.5, '& .MuiSvgIcon-root': { fontSize: 52, opacity: 0.45 } }}>{icon}</Box>}
      <Typography color="text.secondary">{text}</Typography>
      {action && <Box sx={{ mt: 2 }}>{action}</Box>}
    </Box>
  )
}

export function SectionTitle({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mt: 1, mb: 1.5 }}>
      <Typography variant="h6" sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
        {children}
      </Typography>
      {action}
    </Box>
  )
}

export function ComicCard({ comic }: { comic: ComicSummary }) {
  return (
    <Card sx={{ height: '100%', transition: 'transform .18s ease, box-shadow .18s ease', '&:hover': { transform: 'translateY(-3px)', boxShadow: 8 } }}>
      <CardActionArea
        component={RouterLink}
        to={appLink(`/comic/${encodeURIComponent(comic.comic_id)}`)}
        sx={{ display: 'block', height: '100%' }}
      >
        <Box
          sx={{
            position: 'relative',
            paddingTop: '138%',
            overflow: 'hidden',
            bgcolor: 'action.hover',
          }}
        >
          {comic.cover_url ? (
            <CardMedia
              component="img"
              image={imgUrl(comic.cover_url, 320)}
              alt={comic.title}
              loading="lazy"
              decoding="async"
              sx={{ position: 'absolute', top: 0, left: 0, width: '100%', height: '100%', objectFit: 'cover' }}
            />
          ) : (
            <Box
              sx={{
                position: 'absolute',
                top: 0,
                left: 0,
                width: '100%',
                height: '100%',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontSize: 40,
                opacity: 0.3,
              }}
            >
              📖
            </Box>
          )}
        </Box>
        <Box sx={{ p: 1.25 }}>
          <Typography variant="body2" fontWeight={600} noWrap title={comic.title}>
            {comic.title || comic.comic_id}
          </Typography>
          {comic.author ? (
            <Typography variant="caption" color="text.secondary" noWrap display="block">
              {comic.author}
            </Typography>
          ) : null}
        </Box>
      </CardActionArea>
    </Card>
  )
}

const GRID_COLUMNS = { xs: 'repeat(2, 1fr)', sm: 'repeat(3, 1fr)', md: 'repeat(4, 1fr)', lg: 'repeat(6, 1fr)' }

export function ComicGrid({ items }: { items: ComicSummary[] }) {
  return (
    <Box sx={{ display: 'grid', gridTemplateColumns: GRID_COLUMNS, gap: 2 }}>
      {items.map((c) => (
        <ComicCard key={`${c.source}-${c.comic_id}`} comic={c} />
      ))}
    </Box>
  )
}

// 行内条目的稳定 key。必须定义在模块级：如果每次渲染现造一个函数，
// useRowInfinite 内部所有依赖它的 useCallback/effect 都会重建，
// 行尾哨兵的 IntersectionObserver 会被反复重挂。
const comicRowKey = (c: ComicSummary) => `${c.source}-${c.comic_id}`

/**
 * 横向行「滑到右端继续加载」的通用实现。
 *
 * 触发方式是在行尾放一个哨兵元素，用 IntersectionObserver 以「行自身」为 root、
 * 右侧留 800px 提前量：用户还没滑到头就已经把下一页取回来了，所以一直往右滑
 * 都有内容，不会出现「滑到头停住、还得点一下」。
 *
 * 没有 loadMore 的行（上游确实没有更多内容）什么都不做，就停在已有条目上——
 * 这种情况下给「更多」跳走反而更糟，用户要的是在行内继续看。
 */
export function useRowInfinite<T>(
  items: T[],
  keyOf: (item: T) => string,
  loadMore?: (page: number) => Promise<T[]>,
  cap = 120,
): { shown: T[]; loading: boolean; sentinelRef: RefObject<HTMLDivElement | null> } {
  const [extra, setExtra] = useState<T[]>([])
  const [loading, setLoading] = useState(false)
  const [done, setDone] = useState(false)
  const sentinelRef = useRef<HTMLDivElement | null>(null)
  const pageRef = useRef(1)
  const busyRef = useRef(false)
  const idsRef = useRef<Set<string>>(new Set())

  // 行内容整体换掉（切源、重新拉取）时清空追加项，避免旧内容的条目残留在新行里。
  const signature = items.map(keyOf).join('|')
  useEffect(() => {
    setExtra([])
    setDone(false)
    setLoading(false)
    pageRef.current = 1
    busyRef.current = false
  }, [signature])

  const shown = useMemo(() => {
    const seen = new Set<string>()
    const out: T[] = []
    for (const it of items.concat(extra)) {
      const k = keyOf(it)
      if (!k || seen.has(k)) continue
      seen.add(k)
      out.push(it)
    }
    return out
  }, [items, extra, keyOf])

  useEffect(() => {
    idsRef.current = new Set(shown.map(keyOf))
  }, [shown, keyOf])

  const load = useCallback(async () => {
    if (!loadMore || busyRef.current || done) return
    // 到上限就停：行会随追加无限变长，得留个天花板。
    if (idsRef.current.size >= cap) {
      setDone(true)
      return
    }
    busyRef.current = true
    setLoading(true)
    try {
      const got = await loadMore(pageRef.current)
      pageRef.current += 1
      // 去重：分类行的「最新」序列和上游推荐块会重叠。
      const fresh = got.filter((it) => {
        const k = keyOf(it)
        return Boolean(k) && !idsRef.current.has(k)
      })
      if (fresh.length === 0) {
        setDone(true)
        return
      }
      for (const it of fresh) idsRef.current.add(keyOf(it))
      setExtra((prev) => prev.concat(fresh))
    } catch {
      // 出错就停在这一页，不自动重试——否则滑到头会反复打接口。
      setDone(true)
    } finally {
      busyRef.current = false
      setLoading(false)
    }
  }, [loadMore, done, cap, keyOf])

  useEffect(() => {
    const el = sentinelRef.current
    if (!el || !loadMore) return
    if (typeof IntersectionObserver === 'undefined') {
      void load()
      return
    }
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) void load()
      },
      { root: el.parentElement, rootMargin: '0px 800px 0px 0px' },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [load, loadMore])

  // 追加之后行变长了，哨兵若仍在提前量内就接着取（一屏宽可能需要连续取两页）。
  useEffect(() => {
    if (!loadMore || done || loading) return
    const el = sentinelRef.current
    if (!el) return
    const vw = window.innerWidth || 0
    if (el.getBoundingClientRect().left < vw + 800) void load()
  }, [extra, done, loading, load, loadMore])

  return { shown, loading, sentinelRef }
}

/**
 * 横向封面行：scroll-snap + 隐藏滚动条 + iOS 惯性滚动。
 * 首页各分区（编辑推荐 / 排行榜）共用，避免每个页面各写一份。
 */
export function ComicCoverRow({
  items,
  loadMore,
  cap,
}: {
  items: ComicSummary[]
  /** 滑到右端时继续取下一页；不传表示这一行上游没有更多，就停在已有条目上。 */
  loadMore?: (page: number) => Promise<ComicSummary[]>
  cap?: number
}) {
  const { shown, loading, sentinelRef } = useRowInfinite(
    items,
    comicRowKey,
    loadMore,
    cap,
  )
  if (items.length === 0) return null
  return (
    <Box
      sx={{
        display: 'flex',
        gap: 1.5,
        overflowX: 'auto',
        overscrollBehaviorX: 'contain',
        // 用 proximity 而不是 mandatory：这一行有 px:2/mx:-2 的出血内边距，
        // iOS 的强制吸附会拿内边距算吸附点、跟手指拖动互相拉锯，
        // 表现就是「标题写着右滑看更多，但怎么滑都停在原地」（桌面端反而正常）。
        scrollSnapType: 'x proximity',
        WebkitOverflowScrolling: 'touch',
        scrollbarWidth: 'none',
        msOverflowStyle: 'none',
        '&::-webkit-scrollbar': { display: 'none' },
        mx: { xs: -2, sm: -3 },
        px: { xs: 2, sm: 3 },
        pb: 1,
      }}
    >
      {shown.map((c) => (
        <Box
          key={`${c.source}-${c.comic_id}`}
          sx={{ flex: '0 0 auto', width: { xs: 108, sm: 128 }, scrollSnapAlign: 'start' }}
        >
          <ComicCard comic={c} />
        </Box>
      ))}
      {loadMore ? (
        <>
          {loading ? (
            <Box
              sx={{
                flex: '0 0 auto',
                width: { xs: 108, sm: 128 },
                minHeight: 120,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
              }}
            >
              <CircularProgress size={22} thickness={4} />
            </Box>
          ) : null}
          {/* 哨兵：只占 1px，进入右端提前量就触发取下一页 */}
          <Box ref={sentinelRef} sx={{ flex: '0 0 auto', width: '1px', alignSelf: 'stretch' }} />
        </>
      ) : null}
    </Box>
  )
}

export function NovelCard({ novel }: { novel: NovelSummary }) {
  return (
    <Card sx={{ height: '100%', transition: 'transform .18s ease, box-shadow .18s ease', '&:hover': { transform: 'translateY(-3px)', boxShadow: 8 } }}>
      <CardActionArea
        component={RouterLink}
        to={`/novel/${encodeURIComponent(novel.novel_id)}`}
        sx={{ display: 'block', height: '100%' }}
      >
        <Box
          sx={{
            position: 'relative',
            paddingTop: '138%',
            overflow: 'hidden',
            bgcolor: 'action.hover',
          }}
        >
          {novel.cover_url ? (
            <CardMedia
              component="img"
              image={imgUrl(novel.cover_url, 320)}
              alt={novel.title}
              loading="lazy"
              decoding="async"
              sx={{ position: 'absolute', top: 0, left: 0, width: '100%', height: '100%', objectFit: 'cover' }}
            />
          ) : (
            <Box
              sx={{
                position: 'absolute',
                top: 0,
                left: 0,
                width: '100%',
                height: '100%',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontSize: 40,
                opacity: 0.3,
              }}
            >
              📕
            </Box>
          )}
        </Box>
        <Box sx={{ p: 1.25 }}>
          <Typography variant="body2" fontWeight={600} noWrap title={novel.title}>
            {novel.title || novel.novel_id}
          </Typography>
          {novel.author ? (
            <Typography variant="caption" color="text.secondary" noWrap display="block">
              {novel.author}
            </Typography>
          ) : null}
        </Box>
      </CardActionArea>
    </Card>
  )
}

export function NovelGrid({ items }: { items: NovelSummary[] }) {
  return (
    <Box sx={{ display: 'grid', gridTemplateColumns: GRID_COLUMNS, gap: 2 }}>
      {items.map((n) => (
        <NovelCard key={`${n.source}-${n.novel_id}`} novel={n} />
      ))}
    </Box>
  )
}

/** 收藏夹选择项（漫画 / 小说通用）。 */
export interface FavoriteFolder {
  id: string
  name: string
}

/** 收藏夹弹窗确认结果：选择已有收藏夹，或输入新名称直接新建。 */
export type FavoriteFolderChoice =
  | { kind: 'folder'; folderId: string }
  | { kind: 'new'; folderName: string }

/**
 * 可复用的「收藏夹选择弹窗」：打开时拉取收藏夹列表，支持选中已有收藏夹或新建。
 * 具体的新建 / 移动请求由调用方在 onConfirm 内按自身接口完成。
 */
export function FavoriteFolderDialog({
  open,
  title = '选择收藏夹',
  loadFolders,
  submitting = false,
  onClose,
  onConfirm,
}: {
  open: boolean
  title?: string
  loadFolders: () => Promise<FavoriteFolder[]>
  submitting?: boolean
  onClose: () => void
  onConfirm: (choice: FavoriteFolderChoice) => void
}) {
  const [folders, setFolders] = useState<FavoriteFolder[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState('0')
  const [newName, setNewName] = useState('')
  const loaderRef = useRef(loadFolders)
  loaderRef.current = loadFolders

  useEffect(() => {
    if (!open) return
    let alive = true
    setLoading(true)
    setError(null)
    setFolders([])
    setSelected('0')
    setNewName('')
    loaderRef.current().then(
      (list) => {
        if (alive) {
          setFolders(list)
          setLoading(false)
        }
      },
      (err: unknown) => {
        if (alive) {
          setError(err instanceof Error ? err.message : String(err))
          setLoading(false)
        }
      },
    )
    return () => {
      alive = false
    }
  }, [open])

  const canConfirm = newName.trim() !== '' || selected !== ''

  const confirm = () => {
    const name = newName.trim()
    if (name) onConfirm({ kind: 'new', folderName: name })
    else onConfirm({ kind: 'folder', folderId: selected })
  }

  return (
    <Dialog open={open} onClose={submitting ? undefined : onClose} fullWidth maxWidth="xs">
      <DialogTitle>{title}</DialogTitle>
      <DialogContent dividers>
        {loading ? (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
            <CircularProgress size={28} />
          </Box>
        ) : error ? (
          <Alert severity="error">{error}</Alert>
        ) : (
          <>
            <RadioGroup
              value={selected}
              onChange={(e) => {
                setSelected(e.target.value)
                setNewName('')
              }}
            >
              <FormControlLabel value="0" control={<Radio size="small" />} label="默认收藏夹" />
              {folders.map((f) => (
                <FormControlLabel
                  key={f.id}
                  value={f.id}
                  control={<Radio size="small" />}
                  label={f.name || `收藏夹 ${f.id}`}
                />
              ))}
            </RadioGroup>
            <TextField
              label="新建收藏夹"
              placeholder="输入名称后确定将直接新建"
              value={newName}
              onChange={(e) => {
                setNewName(e.target.value)
                setSelected('')
              }}
              fullWidth
              size="small"
              sx={{ mt: 2 }}
            />
          </>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={submitting}>
          取消
        </Button>
        <Button onClick={confirm} variant="contained" disabled={submitting || loading || !!error || !canConfirm}>
          {submitting ? '处理中…' : '确定'}
        </Button>
      </DialogActions>
    </Dialog>
  )
}
