import { Fragment } from 'react'
import { Link as RouterLink } from 'react-router-dom'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import CardMedia from '@mui/material/CardMedia'
import MenuBookIcon from '@mui/icons-material/MenuBook'
import PlayArrowIcon from '@mui/icons-material/PlayArrow'
import Typography from '@mui/material/Typography'
import { api, imgUrl } from '../api'
import { appLink, sourceFromPath, sourcePath, type SourceKind } from '../source'
import {
  CenterLoading,
  ComicCoverRow,
  ErrorState,
  MoreTile,
  NovelCard,
  SectionTitle,
  useAsync,
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

function NovelCoverRow({
  items,
  more,
}: {
  items: NovelSummary[]
  more?: { to: string; label?: string }
}) {
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
      {items.map((n) => (
        <Box
          key={`${n.source}-${n.novel_id}`}
          sx={{ flex: '0 0 auto', width: { xs: 108, sm: 128 }, scrollSnapAlign: 'start' }}
        >
          <NovelCard novel={n} />
        </Box>
      ))}
      {more ? <MoreTile to={more.to} label={more.label} /> : null}
    </Box>
  )
}

export default function Home() {
  const promote = useAsync(loadPromote, [])

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
          const novels = novelListFromPromote(section.content)
          const novelIds = new Set(novels.map((n) => n.novel_id))
          const comics = rawListToSummaries(section.content).filter((c) => !novelIds.has(c.comic_id))
          if (novels.length === 0 && comics.length === 0) return null
          return (
            <Fragment key={key}>
              {novels.length > 0 ? (
                <Box sx={{ mb: 3.5 }}>
                  <SectionTitle>
                    <MenuBookIcon sx={{ color: 'primary.main' }} /> {section.title || '编辑推荐'}
                  </SectionTitle>
                  <NovelCoverRow
                    items={novels}
                    more={
                      // 目标页必须存在于「当前源」：哔咔源没有 /novels。
                      sourceFromPath() === 'bika'
                        ? undefined
                        : { to: appLink('/novels'), label: '全部小说' }
                    }
                  />
                </Box>
              ) : null}
              {comics.length > 0 ? (
                <Box sx={{ mb: 3.5 }}>
                  <SectionTitle>{section.title || '编辑推荐'}</SectionTitle>
                  <ComicCoverRow
                    items={comics}
                    // 行本身没有分页接口，只给出口：能翻页的完整列表页；
                    // 哔咔源没有 /latest，退回分类页，保证链接不会点空。
                    more={{
                      to: appLink(sourceFromPath() === 'bika' ? '/categories' : '/latest'),
                      label: '更多',
                    }}
                  />
                </Box>
              ) : null}
            </Fragment>
          )
        })
      )}
    </Box>
  )
}
