import { Fragment, useCallback } from 'react'
import { Link as RouterLink } from 'react-router-dom'
import Box from '@mui/material/Box'
import CircularProgress from '@mui/material/CircularProgress'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import CardMedia from '@mui/material/CardMedia'
import MenuBookIcon from '@mui/icons-material/MenuBook'
import PlayArrowIcon from '@mui/icons-material/PlayArrow'
import Typography from '@mui/material/Typography'
import { api, imgUrl } from '../api'
import { sourceFromPath, sourcePath, type SourceKind } from '../source'
import {
  CenterLoading,
  ComicCoverRow,
  ErrorState,
  NovelCard,
  SectionTitle,
  useAsync,
  useRowInfinite,
} from '../components'
import type { ComicSummary, NovelSummary } from '../types'
import { useAuth } from '../auth'

const PROMOTE_CACHE_KEY = 'jm.promote.v1'
const PROMOTE_TTL = 10 * 60 * 1000

/** legacy /api/latest 等原始 JM 列表 → v2ComicSummary 的宽松映射 */
export function rawListToSummaries(data: unknown): ComicSummary[] {
  const list = Array.isArray(data) ? data : []
  const out: ComicSummary[] = []
  for (const item of list) {
    if (typeof item !== 'object' || item === null) continue
    const it = item as Record<string, unknown>
    const id = it.id ?? it.album_id ?? it.aid
    if (id === undefined || id === null || String(id) === '') continue
    const authorRaw = it.author
    const rawImage = it.image ? String(it.image) : ''
    let coverUrl: string | null
    if (rawImage.startsWith('http')) {
      coverUrl = rawImage
    } else if (rawImage.startsWith('/')) {
      // JM 接口偶发返回站内相对路径（如小说 /media/novels/68_tmb.jpg），补全 CDN 域名后由卡片统一走代理；
      // 书库路径当前各镜像域名均不可用，置空走占位图避免破图
      coverUrl = rawImage.includes('/media/library/')
        ? null
        : `https://cdn-msp.jmapiproxy2.cc${rawImage}`
    } else {
      // 交回原始 CDN 地址，由卡片统一走图片代理并按需缩略（w 参数）。
      coverUrl = `https://cdn-msp.jmapiproxy2.cc/media/albums/${id}.jpg`
    }
    out.push({
      source: 'jm',
      comic_id: String(id),
      title: String(it.name ?? it.title ?? ''),
      author: Array.isArray(authorRaw)
        ? authorRaw.map(String).join(', ')
        : authorRaw
          ? String(authorRaw)
          : null,
      cover_url: coverUrl,
      tags: [],
    })
  }
  return out
}

/** 官方推荐条目 → 小说摘要：封面路径含 /media/novels/ 的条目视为小说 */
export function novelListFromPromote(data: unknown): NovelSummary[] {
  const list = Array.isArray(data) ? data : []
  const out: NovelSummary[] = []
  for (const item of list) {
    if (typeof item !== 'object' || item === null) continue
    const it = item as Record<string, unknown>
    const id = it.id ?? it.album_id ?? it.aid
    if (id === undefined || id === null || String(id) === '') continue
    const rawImage = it.image ? String(it.image) : ''
    if (!rawImage.includes('/media/novels/')) continue
    const authorRaw = it.author
    out.push({
      source: 'jm',
      novel_id: String(id),
      title: String(it.name ?? it.title ?? ''),
      author: Array.isArray(authorRaw)
        ? authorRaw.map(String).join(', ')
        : authorRaw
          ? String(authorRaw)
          : null,
      cover_url: rawImage.startsWith('http')
        ? rawImage
        : rawImage.startsWith('/')
          ? `https://cdn-msp.jmapiproxy2.cc${rawImage}`
          : null,
      category: null,
    })
  }
  return out
}

/** 官方首页推荐链路（/api/promote）的分区形状 */
interface PromoteSection {
  id?: number | string
  title?: string
  slug?: string
  type?: string
  /** category_id 类型分区的分类号（promote/library/novels 为空或不存在） */
  filter_val?: number | string | null
  content?: unknown[]
}

function normalizeSections(data: unknown): PromoteSection[] {
  if (Array.isArray(data)) return data.filter((s) => s && typeof s === 'object') as PromoteSection[]
  if (data && typeof data === 'object') {
    return Object.values(data as Record<string, unknown>).filter(
      (s) => s && typeof s === 'object' && Array.isArray((s as PromoteSection).content),
    ) as PromoteSection[]
  }
  return []
}

async function loadPromote(): Promise<PromoteSection[]> {
  try {
    const d = await api.get<unknown>('/api/promote')
    const sections = normalizeSections(d)
    if (sections.length > 0) {
      try {
        sessionStorage.setItem(PROMOTE_CACHE_KEY, JSON.stringify({ ts: Date.now(), data: sections }))
      } catch {
        /* 隐私模式等场景写入失败可忽略 */
      }
      return sections
    }
    throw new Error('推荐数据为空')
  } catch (err) {
    try {
      const raw = sessionStorage.getItem(PROMOTE_CACHE_KEY)
      if (raw) {
        const cached = JSON.parse(raw) as { ts: number; data: PromoteSection[] }
        if (Date.now() - cached.ts < PROMOTE_TTL && Array.isArray(cached.data)) return cached.data
      }
    } catch {
      /* 缓存损坏时走错误分支 */
    }
    throw err
  }
}

interface ResumeItem {
  album_id: string
  album_title?: string
  photo_id?: string
  title?: string
  page_index: number
  type?: string
  scroll_pct?: number
  source?: string
  lang?: string
}

function ContinueReading() {
  const { user } = useAuth()
  const last = useAsync<ResumeItem | null>(async () => {
    if (!user) return null
    try {
      const d = await api.get<unknown>(`/api/aura/library/history${api.qs({ limit: 1 })}`)
      const arr = Array.isArray(d) ? (d as ResumeItem[]) : []
      return arr.length > 0 ? arr[0] : null
    } catch {
      return null
    }
  }, [user?.username])

  if (!user || last.loading || last.error || !last.data) return null
  const it = last.data
  const isNovel = it.type === 'novel'
  const itemSource: SourceKind = it.source === 'bika' ? 'bika' : 'jm'
  // JM 封面可由 album_id 直接拼出；其它源历史未存封面，留空而不显示错图。
  const coverPath = isNovel
    ? `https://cdn-msp.jmapiproxy2.cc/media/novels/${it.album_id}.jpg`
    : itemSource === 'jm'
      ? `https://cdn-msp.jmapiproxy2.cc/media/albums/${it.album_id}.jpg`
      : ''
  // 跳转按条目所属源构造，避免从哔咔历史跳到 JM。
  const target = sourcePath(
    itemSource,
    isNovel
      ? `/novel_reader/${encodeURIComponent(it.photo_id || it.album_id)}?nid=${encodeURIComponent(it.album_id)}${it.scroll_pct && it.scroll_pct > 0 ? `&scroll=${it.scroll_pct.toFixed(4)}` : ''}${it.lang ? `&lang=${encodeURIComponent(it.lang)}` : ''}`
      : `/reader/${encodeURIComponent(it.photo_id || it.album_id)}${it.page_index > 0 ? `?page=${it.page_index}` : ''}`,
  )
  return (
    <Card sx={{ mb: 3, borderRadius: 3 }}>
      <CardContent sx={{ display: 'flex', alignItems: 'center', gap: 2, p: { xs: 1.5, sm: 2 } }}>
        <Box
          sx={{
            flex: '0 0 auto',
            width: 72,
            borderRadius: 2,
            overflow: 'hidden',
            bgcolor: 'action.hover',
          }}
        >
          {coverPath ? (
            <CardMedia
              component="img"
              image={imgUrl(coverPath, 160)}
              alt={it.album_title || it.album_id}
              sx={{ width: '100%', height: 'auto', display: 'block' }}
            />
          ) : null}
        </Box>
        <Box sx={{ flexGrow: 1, minWidth: 0 }}>
          <Typography variant="caption" color="text.secondary" sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
            {isNovel ? <MenuBookIcon sx={{ fontSize: 14 }} /> : null}
            继续阅读
          </Typography>
          <Typography variant="subtitle1" fontWeight={600} noWrap>
            {it.title || it.album_title || `作品 ${it.album_id}`}
          </Typography>
          <Typography variant="caption" color="text.secondary">
            {isNovel
              ? it.scroll_pct && it.scroll_pct > 0
                ? `已读 ${Math.round(it.scroll_pct * 100)}%`
                : '刚开始阅读'
              : `上次看到第 ${(it.page_index ?? 0) + 1} 页`}
          </Typography>
        </Box>
        <Button
          variant="contained"
          startIcon={<PlayArrowIcon />}
          component={RouterLink}
          to={target}
          sx={{ flex: '0 0 auto', borderRadius: 3 }}
        >
          继续看
        </Button>
      </CardContent>
    </Card>
  )
}

// 模块级稳定 key（同 ComicCoverRow 的理由：避免每帧重建观察器）。
const novelRowKey = (n: NovelSummary) => `${n.source}-${n.novel_id}`

function NovelCoverRow({
  items,
  loadMore,
}: {
  items: NovelSummary[]
  loadMore?: (page: number) => Promise<NovelSummary[]>
}) {
  const { shown, loading, sentinelRef } = useRowInfinite(items, novelRowKey, loadMore)
  if (items.length === 0) return null
  return (
    <Box
      sx={{
        display: 'flex',
        gap: 1.5,
        overflowX: 'auto',
        overscrollBehaviorX: 'contain',
        // 与 ComicCoverRow 同理：mandatory 吸附 + 出血内边距在 iOS 上会「滑不动」。
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
      {shown.map((n) => (
        <Box
          key={`${n.source}-${n.novel_id}`}
          sx={{ flex: '0 0 auto', width: { xs: 108, sm: 128 }, scrollSnapAlign: 'start' }}
        >
          <NovelCard novel={n} />
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
          <Box ref={sentinelRef} sx={{ flex: '0 0 auto', width: '1px', alignSelf: 'stretch' }} />
        </>
      ) : null}
    </Box>
  )
}

/** 分类分区（type=category_id）的续接：JM 的 `/categories/filter` 就是该分类的最新列表，
 *  后端已暴露为 /api/v2/{source}/leaderboard，80 条一页，不需要新增接口。
 *  注意：上游 /promote 只给每个分区固定的一批（漫画 30 条）且不接受任何分页参数，
 *  所以只有 category_id 类型的分区能真正「继续加载」，其余分区就停在已有条目上。 */
async function loadCategoryPage(source: SourceKind, category: string, page: number): Promise<ComicSummary[]> {
  const d = await api.get<unknown>(`/api/v2/${source}/leaderboard${api.qs({ category, sort: 'mr', page })}`)
  return Array.isArray(d) ? (d as ComicSummary[]) : []
}

/** 小说分区的续接：/promote 的 page 参数只对小说分区有效（每页多 6 条）。 */
async function loadPromoteNovelsPage(page: number): Promise<NovelSummary[]> {
  const d = await api.get<unknown>(`/api/promote${api.qs({ page })}`)
  const sections = normalizeSections(d)
  const sec = sections.find((s) => s.type === 'novels') ?? sections.find((s) => String(s.id) === '1002')
  return sec ? novelListFromPromote(sec.content) : []
}

/** 单个推荐分区。单独抽成组件是为了能按分区 memo 出稳定的 loadMore（不然每次渲染
 *  都会换函数身份，导致行尾哨兵的观察器反复重建）。 */
function PromoteBlock({ section, source }: { section: PromoteSection; source: SourceKind }) {
  const novels = novelListFromPromote(section.content)
  const novelIds = new Set(novels.map((n) => n.novel_id))
  const comics = rawListToSummaries(section.content).filter((c) => !novelIds.has(c.comic_id))
  const fv = section.filter_val
  const category = section.type === 'category_id' && fv !== undefined && fv !== null ? String(fv) : ''
  const loadMoreComics = useCallback(
    (page: number) => (category ? loadCategoryPage(source, category, page) : Promise.resolve([])),
    [source, category],
  )
  if (novels.length === 0 && comics.length === 0) return null
  return (
    <Fragment>
      {novels.length > 0 ? (
        <Box sx={{ mb: 3.5 }}>
          <SectionTitle>
            <MenuBookIcon sx={{ color: 'primary.main' }} /> {section.title || '编辑推荐'}
          </SectionTitle>
          <NovelCoverRow items={novels} loadMore={source === 'bika' ? undefined : loadPromoteNovelsPage} />
        </Box>
      ) : null}
      {comics.length > 0 ? (
        <Box sx={{ mb: 3.5 }}>
          <SectionTitle>{section.title || '编辑推荐'}</SectionTitle>
          <ComicCoverRow items={comics} loadMore={category ? loadMoreComics : undefined} />
        </Box>
      ) : null}
    </Fragment>
  )
}

export default function Home() {
  const promote = useAsync(loadPromote, [])
  const source = sourceFromPath()

  return (
    <Box>
      <ContinueReading />

      {promote.loading ? (
        <CenterLoading />
      ) : promote.error ? (
        <ErrorState message={`推荐加载失败：${promote.error}`} onRetry={promote.reload} />
      ) : (
        (promote.data ?? []).map((section, i) => {
          const key = section.id ?? section.slug ?? i
          // 禁漫書庫（type=library）内容为创作者作品集，既非漫画也非小说，
          // 且其缩略图资源上游已失效；直接跳过，避免渲染成点击后跳到错误作品的卡片。
          if (section.type === 'library') return null
          return <PromoteBlock key={key} section={section} source={source} />
        })
      )}
    </Box>
  )
}
