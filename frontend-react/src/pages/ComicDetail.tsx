import { useEffect, useState } from 'react'
import { Link as RouterLink, useNavigate, useParams } from 'react-router-dom'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import CardMedia from '@mui/material/CardMedia'
import Checkbox from '@mui/material/Checkbox'
import Chip from '@mui/material/Chip'
import CircularProgress from '@mui/material/CircularProgress'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogContent from '@mui/material/DialogContent'
import DialogTitle from '@mui/material/DialogTitle'
import Divider from '@mui/material/Divider'
import FormControlLabel from '@mui/material/FormControlLabel'
import LinearProgress from '@mui/material/LinearProgress'
import Pagination from '@mui/material/Pagination'
import Stack from '@mui/material/Stack'
import TextField from '@mui/material/TextField'
import ToggleButton from '@mui/material/ToggleButton'
import ToggleButtonGroup from '@mui/material/ToggleButtonGroup'
import Typography from '@mui/material/Typography'
import ArrowBackIcon from '@mui/icons-material/ArrowBack'
import BlockIcon from '@mui/icons-material/Block'
import DownloadIcon from '@mui/icons-material/Download'
import FavoriteIcon from '@mui/icons-material/Favorite'
import FavoriteBorderIcon from '@mui/icons-material/FavoriteBorder'
import MenuBookIcon from '@mui/icons-material/MenuBook'
import SendIcon from '@mui/icons-material/Send'
import ThumbUpAltOutlinedIcon from '@mui/icons-material/ThumbUpAltOutlined'
import { api, ApiError, imgUrl } from '../api'
import { useAuth } from '../auth'
import { appLink, commentLikeSupported, sourceFromPath } from '../source'
import { CenterLoading, EmptyState, ErrorState, FavoriteFolderDialog, SectionTitle, useAsync } from '../components'
import type { FavoriteFolder } from '../components'
import { RecommendDialog } from '../components/RecommendDialog'
import { STATUS_LABEL, addEntry, COMIC_DL_LS_KEY, percentOf, statusColor } from '../downloadTasks'
import { useToast } from '../toast'
import type { ChapterSummary, ComicDetail as ComicDetailData, ComicDownloadTask, V2Comment } from '../types'

function CommentNode({
  node,
  onReply,
  depth = 0,
}: {
  node: V2Comment
  onReply: (n: V2Comment) => void
  depth?: number
}) {
  const [liked, setLiked] = useState(false)
  const [busy, setBusy] = useState(false)
  const cid = String(node.CID ?? '')
  const replies = Array.isArray(node.replys) ? node.replys : Array.isArray(node.children) ? node.children : []

  const like = async () => {
    if (!cid || liked || busy) return
    setBusy(true)
    try {
      await api.post(`/api/v2/jm/comment/${encodeURIComponent(cid)}/like`)
      setLiked(true)
    } catch (e) {
      if (e instanceof ApiError && e.st === 1014) window.dispatchEvent(new Event('aura:unauthorized'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Box sx={{ pl: depth > 0 ? 3 : 0 }}>
      <Box
        sx={{
          display: 'flex',
          gap: 1.5,
          py: 1.5,
          px: depth === 0 ? 0 : 1.5,
          borderRadius: 3,
        }}
      >
        <Box
          sx={{
            width: 36,
            height: 36,
            flexShrink: 0,
            borderRadius: '50%',
            bgcolor: 'primary.main',
            color: 'primary.contrastText',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            fontWeight: 700,
            fontSize: 14,
          }}
        >
          {(node.nickname || node.username || 'U').slice(0, 1).toUpperCase()}
        </Box>
        <Box sx={{ minWidth: 0, flex: 1 }}>
          <Typography variant="body2" fontWeight={600}>
            {node.nickname || node.username || '匿名'}
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
            {node.content}
          </Typography>
          <Stack direction="row" spacing={1} alignItems="center" mt={0.5}>
            {commentLikeSupported() && (
              <Button size="small" startIcon={<ThumbUpAltOutlinedIcon fontSize="small" />} disabled={busy || liked} onClick={() => void like()} sx={{ minWidth: 0 }}>
                {node.likes !== undefined && Number(node.likes) > 0 ? Number(node.likes) : '赞'}
              </Button>
            )}
            {depth === 0 && (
              <Button size="small" onClick={() => onReply(node)} sx={{ minWidth: 0 }}>
                回复
              </Button>
            )}
          </Stack>
        </Box>
      </Box>
      {replies.map((c) => (
        <CommentNode key={String(c.CID)} node={c} onReply={onReply} depth={depth + 1} />
      ))}
    </Box>
  )
}

export default function ComicDetail() {
  const { comicId = '' } = useParams()
  const navigate = useNavigate()
  const { user } = useAuth()
  const { toast } = useToast()

  const [favLoading, setFavLoading] = useState(false)
  const [isFav, setIsFav] = useState(false)
  const [folderOpen, setFolderOpen] = useState(false)
  const [folderSaving, setFolderSaving] = useState(false)
  const [dlOpen, setDlOpen] = useState(false)
  const [dlSelected, setDlSelected] = useState<Set<string>>(() => new Set())
  const [dlTask, setDlTask] = useState<ComicDownloadTask | null>(null)
  // 打包模式：false=默认 JPEG（体积小），true=无损 PNG（体积大）。
  const [dlLossless, setDlLossless] = useState(false)
  const [dlStarting, setDlStarting] = useState(false)
  // 无损 PNG 为爱发电赞助者专享：打开下载窗口时查询绑定状态。
  const [dlDonor, setDlDonor] = useState(false)
  const [dlDonorKnown, setDlDonorKnown] = useState(false)
  const [commentPage, setCommentPage] = useState(1)
  const [replyTo, setReplyTo] = useState<V2Comment | null>(null)
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [recommendOpen, setRecommendOpen] = useState(false)
  const [revealed, setRevealed] = useState(false)

  const detail = useAsync(() => api.get<ComicDetailData>(`/api/v2/jm/comic/${encodeURIComponent(comicId)}`), [comicId])
  const comments = useAsync(
    async () => {
      try {
        return await api.get<{ list?: V2Comment[]; total?: number } | V2Comment[]>(
          `/api/v2/jm/comic/${encodeURIComponent(comicId)}/comments${api.qs({ page: commentPage })}`,
        )
      } catch (e) {
        // 未登录时评论区降级为空，不阻塞页面
        if (e instanceof ApiError && e.st === 1014) return null
        throw e
      }
    },
    [comicId, commentPage],
  )

  useEffect(() => {
    if (detail.data) setIsFav(Boolean(detail.data.is_favorite))
  }, [detail.data])

  // 下载任务轮询：仅在有活跃任务且弹窗打开时进行，避免无谓请求。
  useEffect(() => {
    if (!dlOpen || !dlTask) return
    if (!['queued', 'downloading', 'zipping'].includes(dlTask.status)) return
    let alive = true
    const timer = window.setTimeout(() => {
      void (async () => {
        try {
          const raw = await api.get<ComicDownloadTask>(
            `/api/v2/jm/download/tasks/${encodeURIComponent(dlTask.task_id)}`,
          )
          if (alive) setDlTask(raw)
        } catch {
          /* 单次轮询失败静默跳过，下轮重试 */
        }
      })()
    }, 1500)
    return () => {
      alive = false
      window.clearTimeout(timer)
    }
  }, [dlOpen, dlTask])

  if (detail.loading) return <CenterLoading label="加载漫画详情…" />
  if (detail.error)
    return (
      <Box>
        <ErrorState message={detail.error} onRetry={detail.reload} />
        <Button component={RouterLink} to="/" sx={{ mt: 2 }}>
          返回首页
        </Button>
      </Box>
    )
  if (!detail.data) return <EmptyState text="内容不存在" />

  const d = detail.data

  // 一键屏蔽：把一个词写进后端的全局屏蔽词（后端持久化，刷新/其它页面同样生效）。
  const block = async (word: string) => {
    const w = word.trim()
    if (!w) return
    try {
      await api.post('/api/v2/jm/filter/block', { word: w })
      toast('已屏蔽，列表中不再显示')
      navigate(-1)
    } catch (e) {
      toast(e instanceof ApiError ? e.message : '屏蔽失败', 'error')
    }
  }

  // 详情页命中屏蔽且尚未展开：先遮住内容，是否查看交给用户决定。
  if (d.masked && !revealed) {
    return (
      <Box>
        <Button
          startIcon={<ArrowBackIcon />}
          onClick={() => (window.history.length > 1 ? navigate(-1) : navigate(appLink('/')))}
          sx={{ mb: 1.5, ml: -0.5, color: 'text.secondary', textTransform: 'none' }}
        >
          返回
        </Button>
        <Card sx={{ borderRadius: 5 }}>
          <CardContent sx={{ py: 6, textAlign: 'center' }}>
            <BlockIcon sx={{ fontSize: 48, color: 'text.disabled' }} />
            <Typography variant="h6" sx={{ mt: 1 }}>
              该内容已被你的屏蔽规则隐藏
            </Typography>
            <Typography color="text.secondary" sx={{ mt: 0.5 }}>
              命中：{d.masked_reason}
            </Typography>
            <Stack direction="row" spacing={1.5} justifyContent="center" sx={{ mt: 3 }}>
              <Button variant="contained" onClick={() => setRevealed(true)}>
                让我看看
              </Button>
              <Button onClick={() => (window.history.length > 1 ? navigate(-1) : navigate(appLink('/')))}>返回</Button>
            </Stack>
          </CardContent>
        </Card>
      </Box>
    )
  }

  const chapters = d.chapters ?? []
  const coverUrl = imgUrl(d.cover_url, 480) ?? null

  const commentList = Array.isArray(comments.data) ? comments.data : (comments.data?.list ?? [])

  const toggleFav = async () => {
    const desired = !isFav
    setFavLoading(true)
    try {
      const res = await api.post<{
        is_favorite?: boolean
        result?: { type?: string; status?: string; msg?: string }
      }>(`/api/v2/jm/comic/${encodeURIComponent(comicId)}/favorite`, { desired_state: desired })
      const next = typeof res?.is_favorite === 'boolean' ? res.is_favorite : desired
      setIsFav(next)
      toast(next ? '已收藏' : '已取消收藏')
      const opType = String(res?.result?.type ?? '').toLowerCase()
      if (next && (opType === 'add' || opType === 'edit' || opType === 'move')) setFolderOpen(true)
    } catch (e) {
      if (e instanceof ApiError && e.st === 1014) window.dispatchEvent(new Event('aura:unauthorized'))
      else toast(e instanceof ApiError ? e.message : '操作失败', 'error')
    } finally {
      setFavLoading(false)
    }
  }

  const loadFolders = async (): Promise<FavoriteFolder[]> => {
    const d = await api.get<{ folders?: { id: string; name: string }[] }>(
      `/api/favorites${api.qs({ page: 1, folder_id: '0' })}`,
    )
    return (d?.folders ?? []).map((f) => ({ id: String(f.id ?? ''), name: String(f.name ?? '') }))
  }

  const applyFolder = async (choice: { kind: 'folder'; folderId: string } | { kind: 'new'; folderName: string }) => {
    if (choice.kind === 'folder' && (!choice.folderId || choice.folderId === '0')) {
      setFolderOpen(false)
      return
    }
    setFolderSaving(true)
    try {
      const payload =
        choice.kind === 'new'
          ? { type: 'add', folder_name: choice.folderName, album_id: comicId }
          : { type: 'move', folder_id: choice.folderId, album_id: comicId }
      await api.post('/api/favorite_folder', payload)
      toast(choice.kind === 'new' ? '已新建并加入收藏夹' : '已移动到该收藏夹')
      setFolderOpen(false)
    } catch (e) {
      if (e instanceof ApiError && e.st === 1014) window.dispatchEvent(new Event('aura:unauthorized'))
      else toast(e instanceof ApiError ? e.message : '收藏夹操作失败', 'error')
    } finally {
      setFolderSaving(false)
    }
  }

  const openDownload = () => {
    setDlSelected(new Set(chapters.map((c) => c.id)))
    setDlTask(null)
    setDlOpen(true)
    setDlDonorKnown(false)
    api
      .get<{ donor?: boolean }>('/api/afdian/binding')
      .then((d) => {
        const donor = d?.donor === true
        setDlDonor(donor)
        if (!donor) setDlLossless(false)
      })
      .catch(() => {
        setDlDonor(false)
        setDlLossless(false)
      })
      .finally(() => setDlDonorKnown(true))
  }

  const toggleChapter = (id: string, checked: boolean) => {
    setDlSelected((prev) => {
      const next = new Set(prev)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  const startDownload = async () => {
    const picked = chapters.filter((c) => dlSelected.has(c.id))
    if (picked.length === 0) {
      toast('请至少选择一话', 'warning')
      return
    }
    setDlStarting(true)
    try {
      const raw = await api.post<ComicDownloadTask>('/api/v2/jm/download/tasks', {
        comic_id: comicId,
        comic_title: d.title,
        chapters: picked.map((c) => ({ id: c.id, title: c.title || c.id })),
        include_all: picked.length === chapters.length,
        lossless: dlLossless,
      })
      setDlTask(raw)
      addEntry(COMIC_DL_LS_KEY, { task_id: raw.task_id, title: d.title || `#${comicId}` })
      toast('已发起下载，正在解码喵。。。', 'success', {
        label: '查看进度',
        onClick: () => navigate(appLink('/downloads')),
      })
    } catch (e) {
      toast(e instanceof ApiError ? e.message : '创建任务失败', 'error')
    } finally {
      setDlStarting(false)
    }
  }

  const openChapter = (ch: ChapterSummary) => {
    // 尽力记录阅读历史（未登录则忽略）
    if (user) {
      void api
        .post('/api/aura/library/history', {
          album_id: comicId,
          album_title: d.title,
          photo_id: ch.id,
          title: ch.title,
          type: 'comic',
        })
        .catch(() => {})
    }
    navigate(appLink(`/reader/${encodeURIComponent(ch.id)}`))
  }

  const sendComment = async () => {
    const content = draft.trim()
    if (!content) return
    setSending(true)
    try {
      await api.post(`/api/v2/jm/comic/${encodeURIComponent(comicId)}/comments`, {
        content,
        reply_to: replyTo ? String(replyTo.CID ?? '') || undefined : undefined,
      })
      setDraft('')
      setReplyTo(null)
      toast('评论已发送')
      comments.reload()
    } catch (e) {
      toast(e instanceof ApiError ? e.message : '发送失败', 'error')
    } finally {
      setSending(false)
    }
  }

  return (
    <Box>
      <Button
        startIcon={<ArrowBackIcon />}
        onClick={() => (window.history.length > 1 ? navigate(-1) : navigate(appLink('/')))}
        sx={{ mb: 1.5, ml: -0.5, color: 'text.secondary', textTransform: 'none' }}
      >
        返回
      </Button>
      {d.masked && revealed && (
        <Alert
          severity="warning"
          icon={<BlockIcon />}
          sx={{ mb: 1.5, borderRadius: 3 }}
          action={
            <Button color="inherit" size="small" onClick={() => setRevealed(false)}>
              重新隐藏
            </Button>
          }
        >
          该内容命中屏蔽规则（{d.masked_reason}），已按「让我看看」显示。
        </Alert>
      )}
      {/* 头部 */}
      <Card sx={{ borderRadius: 5, overflow: 'hidden' }}>
        <Box sx={{ display: 'flex', flexDirection: { xs: 'column', sm: 'row' }, gap: 3, p: { xs: 2.5, md: 4 } }}>
          {coverUrl && (
            <CardMedia
              component="img"
              image={coverUrl}
              alt={d.title}
              decoding="async"
              sx={{
                width: { xs: '100%', sm: 210 },
                height: 'auto',
                aspectRatio: '3/4',
                objectFit: 'cover',
                borderRadius: 4,
                flexShrink: 0,
                justifySelf: 'center',
              }}
            />
          )}
          <CardContent sx={{ p: 0, flex: 1, '&:last-child': { pb: 0 } }}>
            <Typography variant="h4" component="h1" gutterBottom>
              {d.title || `#${comicId}`}
            </Typography>
            {d.author && (
              <Stack direction="row" alignItems="center" spacing={0.5} mb={1} flexWrap="wrap" useFlexGap>
                <Typography color="text.secondary">作者：{d.author}</Typography>
                <Button
                  size="small"
                  startIcon={<BlockIcon fontSize="small" />}
                  onClick={() => void block(d.author as string)}
                  sx={{ minWidth: 0, textTransform: 'none' }}
                >
                  屏蔽该作者
                </Button>
              </Stack>
            )}
            {d.tags.length > 0 && (
              <>
                <Stack direction="row" flexWrap="wrap" useFlexGap gap={0.75} mb={0.5}>
                  {d.tags.slice(0, 10).map((t) => (
                    <Chip
                      key={t}
                      label={t}
                      size="small"
                      variant="outlined"
                      onClick={() => void block(t)}
                      title={`屏蔽标签「${t}」`}
                      sx={{ cursor: 'pointer' }}
                    />
                  ))}
                </Stack>
                <Typography variant="caption" color="text.disabled" display="block" mb={2}>
                  点击标签或作者可一键屏蔽
                </Typography>
              </>
            )}
            {d.description && (
              <Typography variant="body2" color="text.secondary" sx={{ whiteSpace: 'pre-wrap', mb: 2 }}>
                {d.description}
              </Typography>
            )}
            <Stack
              direction={{ xs: 'column', sm: 'row' }}
              spacing={1.5}
              alignItems={{ xs: 'stretch', sm: 'center' }}
              flexWrap="wrap"
              useFlexGap
            >
              {chapters.length > 0 && (
                <Button
                  variant="contained"
                  size="large"
                  startIcon={<MenuBookIcon />}
                  onClick={() => openChapter(chapters[0]!)}
                  sx={{ borderRadius: 3, width: { xs: '100%', sm: 'auto' }, fontWeight: 700 }}
                >
                  开始阅读
                </Button>
              )}
              <Stack direction="row" spacing={1} justifyContent="center" flexWrap="wrap" useFlexGap>
                <Button
                  variant={isFav ? 'contained' : 'outlined'}
                  color={isFav ? 'error' : 'primary'}
                  startIcon={isFav ? <FavoriteIcon /> : <FavoriteBorderIcon />}
                  onClick={() => void toggleFav()}
                  disabled={favLoading}
                  sx={{ borderRadius: 3, textTransform: 'none' }}
                >
                  {isFav ? '已收藏' : '收藏'}
                </Button>
                {chapters.length > 0 && (
                  <Button
                    variant="outlined"
                    startIcon={<DownloadIcon />}
                    onClick={() => (user ? openDownload() : navigate('/login'))}
                    sx={{ borderRadius: 3, textTransform: 'none' }}
                  >
                    下载
                  </Button>
                )}
                <Button
                  variant="outlined"
                  startIcon={<ThumbUpAltOutlinedIcon />}
                  onClick={() => (user ? setRecommendOpen(true) : navigate('/login'))}
                  sx={{ borderRadius: 3, textTransform: 'none' }}
                >
                  写笔记
                </Button>
                <Button
                  variant="outlined"
                  color="error"
                  startIcon={<BlockIcon />}
                  onClick={() => void block(String(d.comic_id || comicId))}
                  sx={{ borderRadius: 3, textTransform: 'none' }}
                >
                  屏蔽该漫画
                </Button>
              </Stack>
            </Stack>
            {!user && (
              <Alert
                severity="info"
                sx={{ mt: 2, borderRadius: 3 }}
                action={
                  <Button color="inherit" size="small" component={RouterLink} to="/login">
                    去登录
                  </Button>
                }
              >
                登录后可使用收藏、下载与评论功能
              </Alert>
            )}
          </CardContent>
        </Box>
      </Card>

      {/* 章节 */}
      <SectionTitle>📚 章节（{chapters.length}）</SectionTitle>
      {chapters.length === 0 ? (
        <EmptyState text="无章节信息（可能为单话作品）" icon={<MenuBookIcon />} />
      ) : (
        <Box sx={{ display: 'grid', gridTemplateColumns: { xs: 'repeat(2, 1fr)', sm: 'repeat(3, 1fr)', md: 'repeat(4, 1fr)' }, gap: 1.5 }}>
          {chapters.map((ch) => (
            <Button key={ch.id} variant="outlined" onClick={() => openChapter(ch)} sx={{ justifyContent: 'space-between', textTransform: 'none', minHeight: 46 }}>
              <Box component="span" sx={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {ch.title || ch.id}
              </Box>
            </Button>
          ))}
        </Box>
      )}

      {/* 评论 */}
      <SectionTitle>💬 评论</SectionTitle>
      <Box sx={{ mb: 2 }}>
        {replyTo && (
          <Alert
            severity="info"
            sx={{ mb: 1, borderRadius: 3 }}
            onClose={() => setReplyTo(null)}
          >
            回复 @{replyTo.nickname || replyTo.username || '匿名'}
          </Alert>
        )}
        <Stack direction="row" spacing={1.5}>
          <TextField
            fullWidth
            multiline
            maxRows={4}
            size="small"
            placeholder={user ? '写下你的评论…' : '登录后参与评论'}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            disabled={!user || sending}
          />
          <Button variant="contained" endIcon={<SendIcon />} onClick={() => void sendComment()} disabled={!user || sending || !draft.trim()}>
            发送
          </Button>
        </Stack>
      </Box>

      {comments.error ? (
        <ErrorState message={comments.error} onRetry={comments.reload} />
      ) : comments.loading ? (
        <CenterLoading label="加载评论…" />
      ) : commentList.length === 0 ? (
        <Typography color="text.secondary" textAlign="center" py={3}>
          还没有评论，来抢沙发吧
        </Typography>
      ) : (
        <>
          <Divider sx={{ my: 1 }} />
          {commentList.map((c) => (
            <CommentNode key={String(c.CID)} node={c} onReply={setReplyTo} />
          ))}
          <Box sx={{ display: 'flex', justifyContent: 'center', mt: 2 }}>
            <Pagination count={20} page={commentPage} onChange={(_, p) => setCommentPage(p)} color="primary" size="small" />
          </Box>
        </>
      )}

      <Dialog open={dlOpen} onClose={() => setDlOpen(false)} fullWidth maxWidth="sm">
        <DialogTitle>下载漫画</DialogTitle>
        <DialogContent dividers>
          {dlTask ? (
            <Stack spacing={1.5}>
              <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap" useFlexGap>
                <Chip size="small" color={statusColor(dlTask.status)} label={STATUS_LABEL[dlTask.status] ?? dlTask.status} />
                <Typography variant="body2" color="text.secondary">
                  {dlTask.message || dlTask.stage}
                </Typography>
              </Stack>
              <LinearProgress variant={dlTask.percent > 0 ? 'determinate' : 'indeterminate'} value={percentOf(dlTask.percent)} />
              <Stack direction="row" justifyContent="space-between">
                <Typography variant="caption" color="text.secondary">
                  图片 {dlTask.downloaded_images}/{dlTask.total_images}
                </Typography>
                <Typography variant="caption" color="text.secondary">
                  {percentOf(dlTask.percent).toFixed(1)}%
                </Typography>
              </Stack>
              <Typography variant="caption" color="text.secondary">
                可关闭本窗口，任务将在后台继续，进度可到「下载管理」查看。
              </Typography>
            </Stack>
          ) : (
            <Stack spacing={2}>
              <Box>
                <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
                  打包图片质量
                </Typography>
                <ToggleButtonGroup
                  size="small"
                  exclusive
                  fullWidth
                  value={dlLossless ? 'lossless' : 'default'}
                  onChange={(_, v) => {
                    if (v === 'lossless' && !dlDonor) {
                      toast('无损下载为爱发电赞助者专享，请在「设置」中绑定赞助订单号', 'warning')
                      return
                    }
                    if (v) setDlLossless(v === 'lossless')
                  }}
                >
                  <ToggleButton value="default">默认</ToggleButton>
                  <ToggleButton value="lossless" disabled={!dlDonor}>
                    无损模式{dlDonor ? '' : '（赞助专享）'}
                  </ToggleButton>
                </ToggleButtonGroup>
                <Typography variant="caption" color="text.secondary" display="block" mt={0.5}>
                  {dlLossless
                    ? '无损模式：以 PNG 打包，100% 无损，体积约为默认的 4–5 倍。'
                    : '默认模式：JPEG 重编码，体积小、观感与上游一致。'}
                </Typography>
                {dlDonorKnown && !dlDonor ? (
                  <Typography variant="caption" color="text.secondary" display="block" mt={0.5}>
                    无损 PNG 仅爱发电赞助者可用。
                    <Button size="small" component={RouterLink} to={appLink('/settings')} sx={{ ml: 0.5 }}>
                      去绑定订单号
                    </Button>
                  </Typography>
                ) : null}
              </Box>
              <Box>
                <Stack direction="row" alignItems="center" justifyContent="space-between" sx={{ mb: 0.5 }}>
                  <Typography variant="subtitle2">
                    话数（已选 {dlSelected.size}/{chapters.length}）
                  </Typography>
                  <Stack direction="row" spacing={0.5}>
                    <Button size="small" onClick={() => setDlSelected(new Set(chapters.map((c) => c.id)))}>
                      全选
                    </Button>
                    <Button size="small" onClick={() => setDlSelected(new Set<string>())}>
                      清空
                    </Button>
                  </Stack>
                </Stack>
                <Box
                  sx={{
                    display: 'grid',
                    gridTemplateColumns: { xs: '1fr', sm: '1fr 1fr' },
                    gap: 0.25,
                    maxHeight: 300,
                    overflowY: 'auto',
                    border: '1px solid',
                    borderColor: 'divider',
                    borderRadius: 2,
                    p: 1,
                  }}
                >
                  {chapters.map((ch) => (
                    <FormControlLabel
                      key={ch.id}
                      sx={{ mx: 0, minWidth: 0 }}
                      control={
                        <Checkbox size="small" checked={dlSelected.has(ch.id)} onChange={(e) => toggleChapter(ch.id, e.target.checked)} />
                      }
                      label={
                        <Typography variant="body2" noWrap title={ch.title || ch.id}>
                          {ch.title || ch.id}
                        </Typography>
                      }
                    />
                  ))}
                </Box>
              </Box>
            </Stack>
          )}
        </DialogContent>
        <DialogActions>
          {dlTask ? (
            <>
              {dlTask.status === 'completed' && dlTask.download_url ? (
                <Button variant="contained" startIcon={<DownloadIcon />} href={api.url(dlTask.download_url)}>
                  下载压缩包
                </Button>
              ) : null}
              <Button onClick={() => setDlOpen(false)}>关闭</Button>
            </>
          ) : (
            <>
              <Button onClick={() => setDlOpen(false)}>取消</Button>
              <Button
                variant="contained"
                disabled={dlStarting || dlSelected.size === 0}
                startIcon={dlStarting ? <CircularProgress size={16} color="inherit" /> : <DownloadIcon />}
                onClick={() => void startDownload()}
              >
                开始下载
              </Button>
            </>
          )}
        </DialogActions>
      </Dialog>

      <FavoriteFolderDialog
        open={folderOpen}
        loadFolders={loadFolders}
        submitting={folderSaving}
        onClose={() => setFolderOpen(false)}
        onConfirm={(choice) => void applyFolder(choice)}
      />

      <RecommendDialog
        open={recommendOpen}
        onClose={() => setRecommendOpen(false)}
        defaultComicId={comicId}
        defaultKind="comic"
        source={sourceFromPath()}
        onPosted={() => toast('发布成功，可在「社区」页查看')}
      />
    </Box>
  )
}
