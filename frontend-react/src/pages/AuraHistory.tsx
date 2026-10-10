// 阅读历史：GET /api/aura/library/history?limit=N
// 记录形状：{album_id, album_title, photo_id, title, page_index, timestamp, type, scroll_pct}
// 笔记已整合到「社区」页（发布后直接进入社区列表）。
import { Link as RouterLink } from 'react-router-dom'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Divider from '@mui/material/Divider'
import List from '@mui/material/List'
import ListItem from '@mui/material/ListItem'
import ListItemAvatar from '@mui/material/ListItemAvatar'
import Avatar from '@mui/material/Avatar'
import ListItemText from '@mui/material/ListItemText'
import Stack from '@mui/material/Stack'
import HistoryIcon from '@mui/icons-material/History'
import MenuBookIcon from '@mui/icons-material/MenuBook'
import PlayArrowIcon from '@mui/icons-material/PlayArrow'
import { api } from '../api'
import { sourcePath, type SourceKind } from '../source'
import { CenterLoading, EmptyState, ErrorState, SectionTitle, useAsync } from '../components'
import { useAuth } from '../auth'

interface HistoryItem {
  album_id: string
  album_title: string
  photo_id: string
  title: string
  page_index: number
  timestamp: number
  type?: string
  scroll_pct?: number
  source?: string
  lang?: string
}

function formatTime(ms: number): string {
  if (!ms) return '—'
  return new Date(ms).toLocaleString()
}

export default function AuraHistory() {
  const { user } = useAuth()
  const history = useAsync<HistoryItem[]>(async () => {
    const d = await api.get<unknown>(`/api/aura/library/history${api.qs({ limit: 200 })}`)
    return Array.isArray(d) ? (d as HistoryItem[]) : []
  }, [])

  if (!user)
    return (
      <EmptyState
        icon={<HistoryIcon />}
        text="登录 JM 账号后可同步阅读历史"
        action={
          <Button component={RouterLink} to="/login" variant="contained">
            去登录
          </Button>
        }
      />
    )
  if (history.loading) return <CenterLoading />
  if (history.error)
    return <ErrorState message={`阅读历史加载失败：${history.error}`} onRetry={history.reload} />

  const items = history.data ?? []

  return (
    <Stack spacing={1}>
      <SectionTitle>
        <HistoryIcon sx={{ color: 'primary.main' }} /> 阅读历史
      </SectionTitle>
      {items.length === 0 ? (
        <EmptyState icon={<HistoryIcon />} text="还没有阅读记录，去挑一本吧" />
      ) : (
        <List sx={{ bgcolor: 'background.paper', borderRadius: 3, overflow: 'hidden' }}>
          {items.map((it, i) => {
            const isNovel = it.type === 'novel'
            const label = it.title || it.album_title || `作品 ${it.album_id}`
            // 按「该条目所属的源」构造跳转，而不是当前所在源。
            const itemSource: SourceKind = it.source === 'bika' ? 'bika' : 'jm'
            const target = sourcePath(
              itemSource,
              isNovel
                ? `/novel_reader/${encodeURIComponent(it.photo_id || it.album_id)}?nid=${encodeURIComponent(it.album_id)}${it.scroll_pct && it.scroll_pct > 0 ? `&scroll=${it.scroll_pct.toFixed(4)}` : ''}${it.lang ? `&lang=${encodeURIComponent(it.lang)}` : ''}`
                : `/reader/${encodeURIComponent(it.photo_id || it.album_id)}${it.page_index > 0 ? `?page=${it.page_index}` : ''}`,
            )
            const progressLabel = isNovel
              ? it.scroll_pct && it.scroll_pct > 0
                ? `${Math.round(it.scroll_pct * 100)}%`
                : '开头'
              : `第 ${(it.page_index ?? 0) + 1} 页`
            return (
              <Box key={it.album_id}>
                {i > 0 && <Divider variant="inset" component="li" />}
                <ListItem
                  secondaryAction={
                    <Button size="small" variant="contained" startIcon={<PlayArrowIcon />} component={RouterLink} to={target}>
                      继续
                    </Button>
                  }
                >
                  <ListItemAvatar>
                    <Avatar sx={{ bgcolor: isNovel ? 'secondary.main' : 'primary.main', fontSize: 14 }}>
                      {isNovel ? <MenuBookIcon /> : it.page_index > 0 ? `${it.page_index + 1}` : '1'}
                    </Avatar>
                  </ListItemAvatar>
                  <ListItemText
                    primary={label}
                    secondary={
                      <>
                        {it.album_title && it.title && it.album_title !== it.title ? `${it.album_title} · ` : ''}
                        {progressLabel} · {formatTime(it.timestamp)}
                      </>
                    }
                    primaryTypographyProps={{ fontWeight: 600, noWrap: true }}
                  />
                </ListItem>
              </Box>
            )
          })}
        </List>
      )}
    </Stack>
  )
}
