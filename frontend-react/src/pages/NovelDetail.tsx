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
import Typography from '@mui/material/Typography'
import ArrowBackIcon from '@mui/icons-material/ArrowBack'
import BookmarkIcon from '@mui/icons-material/Bookmark'
import BookmarkBorderIcon from '@mui/icons-material/BookmarkBorder'
import DownloadIcon from '@mui/icons-material/Download'
import FavoriteBorderIcon from '@mui/icons-material/FavoriteBorder'
import MenuBookIcon from '@mui/icons-material/MenuBook'
import SendIcon from '@mui/icons-material/Send'
import ThumbUpAltOutlinedIcon from '@mui/icons-material/ThumbUpAltOutlined'
import { api, ApiError, imgUrl } from '../api'
import { useAuth } from '../auth'
import { CenterLoading, EmptyState, ErrorState, FavoriteFolderDialog, SectionTitle, useAsync } from '../components'
import type { FavoriteFolder } from '../components'
import { RecommendDialog } from '../components/RecommendDialog'
import { NOVEL_EXPORT_LS_KEY, STATUS_LABEL, addEntry, percentOf, statusColor } from '../downloadTasks'
import { useToast } from '../toast'
import type { NovelChapterSummary, NovelDetail as NovelDetailData, NovelExportTask, V2Comment } from '../types'

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
      <Box sx={{ display: 'flex', gap: 1.5, py: 1.5, px: depth === 0 ? 0 : 1.5, borderRadius: 3 }}>
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
            <Button size="small" startIcon={<ThumbUpAltOutlinedIcon fontSize="small" />} disabled={busy || liked} onClick={() => void like()} sx={{ minWidth: 0 }}>
              {node.likes !== undefined && Number(node.likes) > 0 ? Number(node.likes) : '赞'}
            </Button>
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

export default function NovelDetail() {
  const { novelId = '' } = useParams()
  const navigate = useNavigate()
  const { user } = useAuth()
  const { toast } = useToast()

  const [likeLoading, setLikeLoading] = useState(false)
  const [isFav, setIsFav] = useState(false)
  const [favLoading, setFavLoading] = useState(false)
  const [folderOpen, setFolderOpen] = useState(false)
  const [recommendOpen, setRecommendOpen] = useState(false)
  const [folderSaving, setFolderSaving] = useState(false)
  const [commentPage, setCommentPage] = useState(1)
  const [replyTo, setReplyTo] = useState<V2Comment | null>(null)
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)

  const [dlOpen, setDlOpen] = useState(false)
  const [dlFormat, setDlFormat] = useState<'txt' | 'epub'>('txt')
  const [dlLang, setDlLang] = useState<'tw' | 'cn'>('tw')
  const [dlSelected, setDlSelected] = useState<Set<string>>(() => new Set())
  const [dlTask, setDlTask] = useState<NovelExportTask | null>(null)
  const [dlStarting, setDlStarting] = useState(false)

  const detail = useAsync(() => api.get<NovelDetailData>(`/api/v2/jm/novel/${encodeURIComponent(novelId)}`), [novelId])

  useEffect(() => {
    if (detail.data) setIsFav(Boolean(detail.data.is_favorite))
  }, [detail.data])

  const comments = useAsync(
    async () => {
      try {
        return await api.get<{ list?: V2Comment[]; total?: number } | V2Comment[]>(
          `/api/v2/jm/novel/${encodeURIComponent(novelId)}/comments${api.qs({ page: commentPage })}`,
        )
      } catch (e) {
        if (e instanceof ApiError && e.st === 1014) return null
        throw e
      }
    },
    [novelId, commentPage],
  )

  // 导出任务轮询：仅在有活跃任务且弹窗打开时进行，避免无谓请求。
  useEffect(() => {
    if (!dlOpen || !dlTask) return
    if (!['queued', 'downloading', 'zipping'].includes(dlTask.status)) return
    let alive = true
    const timer = window.setTimeout(() => {
      void (async () => {
        try {
          const raw = await api.get<NovelExportTask>(
            `/api/v2/jm/novel/export/tasks/${encodeURIComponent(dlTask.task_id)}`,
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

  if (detail.loading) return <CenterLoading label="加载小说详情…" />
  if (detail.error)
    return (
      <Box>
        <ErrorState message={detail.error} onRetry={detail.reload} />
        <Button component={RouterLink} to="/novels" sx={{ mt: 2 }}>
          返回小说列表
        </Button>
      </Box>
    )
  if (!detail.data) return <EmptyState text="小说不存在" />

  const d = detail.data
  const chapters = d.chapters ?? []
  const coverUrl = imgUrl(d.cover_url, 480) ?? null

  const commentList = Array.isArray(comments.data) ? comments.data : (comments.data?.list ?? [])

  const openDownload = () => {
    setDlSelected(new Set(chapters.map((c) => c.id)))
    setDlTask(null)
    setDlOpen(true)
  }

  const toggleChapter = (id: string, checked: boolean) => {
    setDlSelected((prev) => {
      const next = new Set(prev)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  const startExport = async () => {
    const picked = chapters.filter((c) => dlSelected.has(c.id))
    if (picked.length === 0) {
      toast('请至少选择一章', 'warning')
      return
    }
    setDlStarting(true)
    try {
      const raw = await api.post<NovelExportTask>('/api/v2/jm/novel/export/tasks', {
        novel_id: novelId,
        novel_title: d.title,
        author: d.author ?? undefined,
        chapters: picked.map((c) => ({ id: c.id, title: c.title || c.id })),
        include_all: picked.length === chapters.length,
        lang: dlLang,
        format: dlFormat,
      })
      setDlTask(raw)
      addEntry(NOVEL_EXPORT_LS_KEY, { task_id: raw.task_id, title: d.title || `#${novelId}` })
      toast('已发起下载，正在解码喵。。。', 'success', {
        label: '查看进度',
        onClick: () => navigate('/downloads'),
      })
    } catch (e) {
      toast(e instanceof ApiError ? e.message : '创建导出任务失败', 'error')
    } finally {
      setDlStarting(false)
    }
  }

  const likeNovel = async () => {
    setLikeLoading(true)
    try {
      await api.post(`/api/v2/jm/novel/${encodeURIComponent(novelId)}/like`)
      toast('已点赞')
    } catch (e) {
      toast(e instanceof ApiError ? e.message : '操作失败', 'error')
    } finally {
      setLikeLoading(false)
    }
  }

  const toggleFav = async () => {
    const desired = !isFav
    setFavLoading(true)
    try {
      const res = await api.post<{
        is_favorite?: boolean
        result?: { type?: string; status?: string; msg?: string }
      }>(`/api/v2/jm/novel/${encodeURIComponent(novelId)}/favorite`, { desired_state: desired })
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
      `/api/v2/jm/novel_favorites${api.qs({ page: 1 })}`,
    )
    return (d?.folders ?? []).map((f) => ({ id: String(f.id ?? ''), name: String(f.name ?? '') }))
  }

  const applyFolder = async (choice: { kind: 'folder'; folderId: string } | { kind: 'new'; folderName: string }) => {
    if (choice.kind === 'folder' && !choice.folderId) {
      setFolderOpen(false)
      return
    }
    setFolderSaving(true)
    try {
      const payload =
        choice.kind === 'new'
          ? { type: 'add', folder_name: choice.folderName }
          : { type: 'move', folder_id: choice.folderId }
      await api.post(`/api/v2/jm/novel/${encodeURIComponent(novelId)}/favorite_folder`, payload)
      toast(choice.kind === 'new' ? '已新建并加入收藏夹' : '已移动到该收藏夹')
      setFolderOpen(false)
    } catch (e) {
      if (e instanceof ApiError && e.st === 1014) window.dispatchEvent(new Event('aura:unauthorized'))
      else toast(e instanceof ApiError ? e.message : '收藏夹操作失败', 'error')
    } finally {
      setFolderSaving(false)
    }
  }

  const openChapter = (ch: NovelChapterSummary) => {
    navigate(`/novel_reader/${encodeURIComponent(ch.id)}?nid=${encodeURIComponent(novelId)}`)
  }

  const sendComment = async () => {
    const content = draft.trim()
    if (!content) return
    setSending(true)
    try {
      await api.post(`/api/v2/jm/novel/${encodeURIComponent(novelId)}/comments`, {
        content,
        comment_id: replyTo ? String(replyTo.CID ?? '') || undefined : undefined,
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
        onClick={() => (window.history.length > 1 ? navigate(-1) : navigate('/novels'))}
        sx={{ mb: 1.5, ml: -0.5, color: 'text.secondary', textTransform: 'none' }}
      >
        返回
      </Button>
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
              {d.title || `#${novelId}`}
            </Typography>
            {d.author && (
              <Typography color="text.secondary" mb={1}>
                作者：{d.author}
              </Typography>
            )}
            {d.tags.length > 0 && (
              <Stack direction="row" flexWrap="wrap" useFlexGap gap={0.75} mb={2}>
                {d.tags.slice(0, 10).map((t) => (
                  <Chip key={t} label={t} size="small" variant="outlined" />
                ))}
              </Stack>
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
                  color={isFav ? 'secondary' : 'primary'}
                  startIcon={isFav ? <BookmarkIcon /> : <BookmarkBorderIcon />}
                  onClick={() => void toggleFav()}
                  disabled={favLoading}
                  sx={{ borderRadius: 3, textTransform: 'none' }}
                >
                  {favLoading ? <CircularProgress size={18} /> : isFav ? '已收藏' : '收藏'}
                </Button>
                <Button
                  variant="outlined"
                  startIcon={<FavoriteBorderIcon />}
                  onClick={() => void likeNovel()}
                  disabled={likeLoading}
                  sx={{ borderRadius: 3, textTransform: 'none' }}
                >
                  {likeLoading ? <CircularProgress size={18} /> : '点赞'}
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
                登录后可使用点赞与评论功能
              </Alert>
            )}
          </CardContent>
        </Box>
      </Card>

      <SectionTitle>📖 章节（{chapters.length}）</SectionTitle>
      {chapters.length === 0 ? (
        <EmptyState text="无章节信息" icon={<MenuBookIcon />} />
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

      <SectionTitle>💬 评论</SectionTitle>
      <Box sx={{ mb: 2 }}>
        {replyTo && (
          <Alert severity="info" sx={{ mb: 1, borderRadius: 3 }} onClose={() => setReplyTo(null)}>
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
        <DialogTitle>下载小说</DialogTitle>
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
                  章节 {dlTask.downloaded_chapters}/{dlTask.total_chapters}
                  {dlTask.failed_chapters > 0 ? ` · 失败 ${dlTask.failed_chapters}` : ''}
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
                <Typography variant="subtitle2" gutterBottom>
                  格式
                </Typography>
                <Stack direction="row" spacing={1}>
                  {(['txt', 'epub'] as const).map((f) => (
                    <Button key={f} size="small" variant={dlFormat === f ? 'contained' : 'outlined'} onClick={() => setDlFormat(f)}>
                      {f.toUpperCase()}
                    </Button>
                  ))}
                </Stack>
              </Box>
              <Box>
                <Typography variant="subtitle2" gutterBottom>
                  字体
                </Typography>
                <Stack direction="row" spacing={1}>
                  <Button size="small" variant={dlLang === 'tw' ? 'contained' : 'outlined'} onClick={() => setDlLang('tw')}>
                    繁体
                  </Button>
                  <Button size="small" variant={dlLang === 'cn' ? 'contained' : 'outlined'} onClick={() => setDlLang('cn')}>
                    简体
                  </Button>
                </Stack>
              </Box>
              <Box>
                <Stack direction="row" alignItems="center" justifyContent="space-between" sx={{ mb: 0.5 }}>
                  <Typography variant="subtitle2">
                    章节（已选 {dlSelected.size}/{chapters.length}）
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
                  下载 {dlTask.format === 'epub' ? 'EPUB' : 'TXT'}
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
                onClick={() => void startExport()}
              >
                开始导出
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
        defaultComicId={novelId}
        defaultKind="novel"
        onPosted={() => toast('发布成功，可在「社区」页查看')}
      />
    </Box>
  )
}
