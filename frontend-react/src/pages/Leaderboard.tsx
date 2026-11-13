import { useState } from 'react'
import Box from '@mui/material/Box'
import MenuItem from '@mui/material/MenuItem'
import Typography from '@mui/material/Typography'
import FormControl from '@mui/material/FormControl'
import InputLabel from '@mui/material/InputLabel'
import Pagination from '@mui/material/Pagination'
import Select from '@mui/material/Select'
import Stack from '@mui/material/Stack'
import ToggleButton from '@mui/material/ToggleButton'
import ToggleButtonGroup from '@mui/material/ToggleButtonGroup'
import WhatshotIcon from '@mui/icons-material/Whatshot'
import { api } from '../api'
import { sourceFromPath } from '../source'
import { ComicGrid, CenterLoading, EmptyState, ErrorState, useAsync } from '../components'
import type { ComicSummary } from '../types'

const SORTS = [
  { id: 'tf', title: '最多爱心' },
  { id: 'mv', title: '最多观看' },
  { id: 'mr', title: '最近更新' },
]

// 哔咔榜单的时间段，与 App 端 LeaderboardTimeframe 枚举一一对应：
// H24=24小时 / D7=7天 / D30=30天（tt 参数）。
const BIKA_PERIODS = [
  { id: 'H24', title: '24小时' },
  { id: 'D7', title: '7天' },
  { id: 'D30', title: '30天' },
]

interface RawCategory {
  id?: unknown
  category_id?: unknown
  title?: unknown
  name?: unknown
  category?: unknown
}

/**
 * 哔咔排行榜。
 *
 * 上游接口是 GET comics/leaderboard?tt={H24|D7|D30}&ct=VC：**定长列表、不分页**，
 * 榜单类型目前只有「浏览榜」一种，所以这里的维度只有时间段一个。
 * 后端 10 分钟内复用同一份结果（见 provider_bika.go），频繁切换时间段不会打上游。
 */
function BikaLeaderboard() {
  const [period, setPeriod] = useState('H24')

  const list = useAsync(
    () => api.get<ComicSummary[]>(`/api/v2/jm/leaderboard${api.qs({ period, category: 'VC' })}`),
    [period],
  )

  return (
    <>
      <Stack direction="row" alignItems="center" spacing={1.5} mb={2} flexWrap="wrap" useFlexGap>
        <ToggleButtonGroup
          size="small"
          exclusive
          value={period}
          onChange={(_, v: string | null) => {
            if (v) setPeriod(v)
          }}
          aria-label="榜单时间段"
        >
          {BIKA_PERIODS.map((p) => (
            <ToggleButton key={p.id} value={p.id} sx={{ px: 2, py: 0.5, textTransform: 'none' }}>
              {p.title}
            </ToggleButton>
          ))}
        </ToggleButtonGroup>
        <Typography variant="caption" color="text.secondary">
          浏览榜 · 按{BIKA_PERIODS.find((p) => p.id === period)?.title ?? ''}浏览热度排序
        </Typography>
      </Stack>

      {list.loading ? (
        <CenterLoading />
      ) : list.error ? (
        <ErrorState message={list.error} onRetry={list.reload} />
      ) : !list.data || list.data.length === 0 ? (
        <EmptyState text="这个时间段还没有上榜的内容" icon={<WhatshotIcon />} />
      ) : (
        <ComicGrid items={list.data} ranked />
      )}
    </>
  )
}

/** JM 排行榜：分类 × 排序 × 分页，保持不变。 */
function JmLeaderboard() {
  const [sort, setSort] = useState('tf')
  const [category, setCategory] = useState('0')
  const [page, setPage] = useState(1)

  const cats = useAsync(async () => {
    try {
      return await api.get<RawCategory[]>('/api/v2/jm/categories')
    } catch {
      return [] as RawCategory[]
    }
  }, [])

  const list = useAsync(
    () => api.get<ComicSummary[]>(`/api/v2/jm/leaderboard${api.qs({ sort, category, page })}`),
    [sort, category, page],
  )

  const catOptions = (cats.data ?? []).map((c) => ({
    id: String(c.id ?? c.category_id ?? ''),
    title: String(c.title ?? c.name ?? c.category ?? ''),
  }))

  return (
    <>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} mb={3}>
        <FormControl size="small" sx={{ minWidth: 150 }}>
          <InputLabel>排序</InputLabel>
          <Select
            value={sort}
            label="排序"
            onChange={(e) => {
              setSort(e.target.value)
              setPage(1)
            }}
          >
            {SORTS.map((s) => (
              <MenuItem key={s.id} value={s.id}>
                {s.title}
              </MenuItem>
            ))}
          </Select>
        </FormControl>
        <FormControl size="small" sx={{ minWidth: 180 }}>
          <InputLabel>分类</InputLabel>
          <Select
            value={category}
            label="分类"
            onChange={(e) => {
              setCategory(e.target.value)
              setPage(1)
            }}
          >
            <MenuItem value="0">全部分类</MenuItem>
            {catOptions
              .filter((c) => c.id !== '' && c.id !== '0')
              .map((c) => (
                <MenuItem key={c.id} value={c.id}>
                  {c.title}
                </MenuItem>
              ))}
          </Select>
        </FormControl>
      </Stack>

      {list.loading ? (
        <CenterLoading />
      ) : list.error ? (
        <ErrorState message={list.error} onRetry={list.reload} />
      ) : !list.data || list.data.length === 0 ? (
        <EmptyState text="没有找到内容" icon={<WhatshotIcon />} />
      ) : (
        <>
          <ComicGrid items={list.data} />
          <Box sx={{ display: 'flex', justifyContent: 'center', mt: 4 }}>
            <Pagination
              count={50}
              page={page}
              onChange={(_, p) => {
                setPage(p)
                window.scrollTo({ top: 0 })
              }}
              color="primary"
            />
          </Box>
        </>
      )}
    </>
  )
}

export default function Leaderboard({ title = '排行榜' }: { title?: string } = {}) {
  const isBika = sourceFromPath() === 'bika'

  return (
    <Box>
      <Typography variant="h5" fontWeight={800} mb={2} sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
        <WhatshotIcon color="primary" /> {title}
      </Typography>
      {isBika ? <BikaLeaderboard /> : <JmLeaderboard />}
    </Box>
  )
}