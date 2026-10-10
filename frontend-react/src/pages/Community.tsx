import { memo, useCallback, useEffect, useRef, useState } from 'react'
import { Link as RouterLink, useNavigate, useSearchParams } from 'react-router-dom'
import Avatar from '@mui/material/Avatar'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import Chip from '@mui/material/Chip'
import CircularProgress from '@mui/material/CircularProgress'
import Divider from '@mui/material/Divider'
import IconButton from '@mui/material/IconButton'
import Pagination from '@mui/material/Pagination'
import Stack from '@mui/material/Stack'
import TextField from '@mui/material/TextField'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import AddIcon from '@mui/icons-material/Add'
import ChatBubbleOutlineIcon from '@mui/icons-material/ChatBubbleOutline'
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutline'
import ForumOutlinedIcon from '@mui/icons-material/ForumOutlined'
import SendIcon from '@mui/icons-material/Send'
import ThumbUpIcon from '@mui/icons-material/ThumbUp'
import ThumbUpOutlinedIcon from '@mui/icons-material/ThumbUpOutlined'
import { ApiError, imgUrl } from '../api'
import { useAuth } from '../auth'
import { COMMENT_BODY_MAX, communityApi, timeAgo } from '../community'
import { CenterLoading, EmptyState, ErrorState, useAsync } from '../components'
import { RecommendDialog } from '../components/RecommendDialog'
import { useToast } from '../toast'
import { sourcePath, type SourceKind } from '../source'
import type { RecommendComment, Recommendation, RecommendationList, WorkKind } from '../types'

const PAGE_SIZE = 10
const COMMENT_PAGE_SIZE = 10

/** 作品类型文字标签：漫画 / 小说。 */
function KindTag({ kind }: { kind: WorkKind }) {
  const isNovel = kind === 'novel'
  return (
    <Chip
      label={isNovel ? '小说' : '漫画'}
      size="small"
      variant="outlined"
      color={isNovel ? 'secondary' : 'default'}
      sx={{ height: 18, fontSize: 11, flex: '0 0 auto', '& .MuiChip-label': { px: 0.75, py: 0 } }}
    />
  )
}

function workTo(source: SourceKind, kind: WorkKind, id: string): string {
  const logical = kind === 'novel' ? `/novel/${encodeURIComponent(id)}` : `/comic/${encodeURIComponent(id)}`
  return sourcePath(source, logical)
}

function workLabel(kind: WorkKind, id: string, title: string): string {
  if (title) return title
  return kind === 'novel' ? `小说 ${id}` : `本子 ${id}`
}

/** 帖子引用的外链图片。最多 9 张、只在需要时解码；单图给大图，多图走方形网格。 */
const PostImages = memo(function PostImages({ images }: { images: string[] }) {
  if (images.length === 0) return null
  const single = images.length === 1
  return (
    <Box
      sx={{
        mt: 1,
        display: 'grid',
        gap: 0.75,
        gridTemplateColumns: single ? 'minmax(0, 320px)' : 'repeat(auto-fill, minmax(108px, 1fr))',
      }}
    >
      {images.map((src) => (
        <Box
          key={src}
          component="a"
          href={src}
          target="_blank"
          rel="noreferrer noopener"
          sx={{
            display: 'block',
            borderRadius: 1.5,
            overflow: 'hidden',
            bgcolor: 'action.hover',
            aspectRatio: single ? undefined : '1 / 1',
          }}
        >
          <Box
            component="img"
            src={src}
            alt=""
            loading="lazy"
            decoding="async"
            referrerPolicy="no-referrer"
            sx={{
              display: 'block',
              width: '100%',
              height: single ? 'auto' : '100%',
              maxHeight: single ? 340 : undefined,
              objectFit: 'cover',
            }}
          />
        </Box>
      ))}
    </Box>
  )
})

/** 评论区：只有展开时才挂载并拉取，折叠后不占用任何资源。 */
const CommentThread = memo(function CommentThread({
  postId,
  onCountChange,
}: {
  postId: string
  onCountChange: (n: number) => void
}) {
  const { user } = useAuth()
  const { toast } = useToast()
  const [items, setItems] = useState<RecommendComment[]>([])
  const [total, setTotal] = useState(0)
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState('')
  const [text, setText] = useState('')
  const [sending, setSending] = useState(false)
  const loadedPages = useRef(0)

  const loadPage = useCallback(
    async (page: number) => {
      const data = await communityApi.comments(postId, page, COMMENT_PAGE_SIZE)
      setItems((prev) => (page <= 1 ? data.items : [...prev, ...data.items]))
      setTotal(data.total)
      setHasMore(data.has_more)
      loadedPages.current = page
      onCountChange(data.total)
    },
    [postId, onCountChange],
  )

  useEffect(() => {
    let alive = true
    setLoading(true)
    setError('')
    loadPage(1)
      .catch((e) => {
        if (alive) setError(e instanceof Error ? e.message : '评论加载失败')
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [loadPage])

  const more = async () => {
    setLoadingMore(true)
    try {
      await loadPage(loadedPages.current + 1)
    } catch (e) {
      toast(e instanceof Error ? e.message : '加载失败', 'error')
    } finally {
      setLoadingMore(false)
    }
  }

  const send = async () => {
    const body = text.trim()
    if (!body) return
    setSending(true)
    try {
      const c = await communityApi.comment(postId, body)
      setItems((prev) => [c, ...prev])
      setText('')
      const nextTotal = total + 1
      setTotal(nextTotal)
      onCountChange(nextTotal)
    } catch (e) {
      toast(e instanceof ApiError ? e.message : '评论失败', 'error')
    } finally {
      setSending(false)
    }
  }

  const removeComment = async (id: string) => {
    if (!window.confirm('确定删除这条评论吗？')) return
    try {
      await communityApi.removeComment(id)
      toast('已删除')
      setItems((prev) => prev.filter((c) => c.id !== id))
      const nextTotal = Math.max(0, total - 1)
      setTotal(nextTotal)
      onCountChange(nextTotal)
    } catch (e) {
      toast(e instanceof Error ? e.message : '删除失败', 'error')
    }
  }

  return (
    <Box sx={{ mt: 1.25, pl: { xs: 0.5, sm: 3.5 } }}>
      <Divider sx={{ mb: 1 }} />
      {loading ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 1.5 }}>
          <CircularProgress size={16} />
        </Box>
      ) : error ? (
        <Typography variant="caption" color="error">
          {error}
        </Typography>
      ) : items.length === 0 ? (
        <Typography variant="caption" color="text.disabled">
          还没有评论
        </Typography>
      ) : (
        <Stack spacing={1}>
          {items.map((c) => (
            <Stack key={c.id} direction="row" spacing={0.75} alignItems="flex-start">
              <Box sx={{ minWidth: 0, flex: 1 }}>
                <Typography variant="caption" color="text.secondary">
                  {c.author_name || c.author} · {timeAgo(c.created_at)}
                </Typography>
                <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                  {c.body}
                </Typography>
              </Box>
              {c.can_delete && (
                <Tooltip title="删除">
                  <IconButton size="small" onClick={() => void removeComment(c.id)}>
                    <DeleteOutlineIcon sx={{ fontSize: 16 }} />
                  </IconButton>
                </Tooltip>
              )}
            </Stack>
          ))}
          {hasMore && (
            <Box>
              <Button size="small" onClick={() => void more()} disabled={loadingMore}>
                {loadingMore ? '加载中…' : '加载更多评论'}
              </Button>
            </Box>
          )}
        </Stack>
      )}

      {user ? (
        <Stack direction="row" spacing={1} alignItems="flex-end" sx={{ mt: 1.25 }}>
          <TextField
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="友善评论…"
            size="small"
            fullWidth
            multiline
            maxRows={4}
            disabled={sending}
            slotProps={{ htmlInput: { maxLength: COMMENT_BODY_MAX } }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) void send()
            }}
          />
          <IconButton color="primary" onClick={() => void send()} disabled={sending || !text.trim()}>
            <SendIcon fontSize="small" />
          </IconButton>
        </Stack>
      ) : (
        <Typography variant="caption" color="text.disabled" sx={{ display: 'block', mt: 1 }}>
          登录后即可评论
        </Typography>
      )}
    </Box>
  )
})

/** 一条帖子。点赞 / 评论数都保存在卡片内部：一次点赞只会重渲染这一张卡片。 */
const CommunityPost = memo(function CommunityPost({
  post,
  onDelete,
  onNeedLogin,
}: {
  post: Recommendation
  onDelete: (id: string) => void
  onNeedLogin: () => void
}) {
  const { user } = useAuth()
  const { toast } = useToast()
  const [liked, setLiked] = useState(post.liked_by_me)
  const [likeCount, setLikeCount] = useState(post.like_count)
  const [commentCount, setCommentCount] = useState(post.comment_count)
  const [commentsOpen, setCommentsOpen] = useState(false)
  const [liking, setLiking] = useState(false)

  const src: SourceKind = post.source === 'bika' ? 'bika' : 'jm'
  const name = post.author_name || post.author
  const cover = post.comic_id ? imgUrl(post.cover_url, 320) : null
  const href = post.comic_id ? workTo(src, post.kind, post.comic_id) : ''
  const label = post.comic_id ? workLabel(post.kind, post.comic_id, post.comic_title) : ''

  const toggleLike = async () => {
    if (!user) {
      onNeedLogin()
      return
    }
    if (liking) return
    setLiking(true)
    const prevLiked = liked
    const prevCount = likeCount
    const next = !prevLiked
    setLiked(next)
    setLikeCount(Math.max(0, prevCount + (next ? 1 : -1)))
    try {
      const r = await communityApi.like(post.id)
      setLiked(r.liked)
      setLikeCount(r.like_count)
    } catch (e) {
      setLiked(prevLiked)
      setLikeCount(prevCount)
      toast(e instanceof ApiError ? e.message : '操作失败', 'error')
    } finally {
      setLiking(false)
    }
  }

  const toggleComments = () => {
    setCommentsOpen((v) => !v)
  }

  const onCountChange = useCallback((n: number) => setCommentCount(n), [])

  return (
    <Card sx={{ p: 1.75 }}>
      <Stack direction="row" spacing={1} alignItems="center">
        <Avatar sx={{ width: 28, height: 28, fontSize: 13 }}>{name.slice(0, 1).toUpperCase()}</Avatar>
        <Box sx={{ minWidth: 0, flex: 1 }}>
          <Typography variant="subtitle2" fontWeight={700} noWrap>
            {name}
          </Typography>
          <Typography variant="caption" color="text.disabled">
            {timeAgo(post.created_at)}
          </Typography>
        </Box>
        {post.can_delete && (
          <Tooltip title="删除">
            <IconButton size="small" onClick={() => onDelete(post.id)}>
              <DeleteOutlineIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        )}
      </Stack>

      {post.body && (
        <Typography variant="body2" sx={{ mt: 1, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
          {post.body}
        </Typography>
      )}

      <PostImages images={post.images} />

      {post.comic_id && (
        <Box
          component={RouterLink}
          to={href}
          sx={{
            mt: 1,
            display: 'flex',
            alignItems: 'center',
            gap: 1,
            p: 0.75,
            borderRadius: 1.5,
            bgcolor: 'action.hover',
            color: 'inherit',
            textDecoration: 'none',
            '&:hover': { bgcolor: 'action.selected' },
          }}
        >
          <Box
            sx={{
              flex: '0 0 auto',
              width: 40,
              height: 54,
              borderRadius: 1,
              overflow: 'hidden',
              bgcolor: 'background.paper',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              fontSize: 18,
            }}
          >
            {cover ? (
              <Box
                component="img"
                src={cover}
                alt={label}
                loading="lazy"
                decoding="async"
                sx={{ width: '100%', height: '100%', objectFit: 'cover' }}
              />
            ) : (
              '📖'
            )}
          </Box>
          <Box sx={{ minWidth: 0, flex: 1 }}>
            <Stack direction="row" spacing={0.5} alignItems="center">
              <KindTag kind={post.kind} />
              <Typography variant="caption" color="text.secondary">
                推荐作品
              </Typography>
            </Stack>
            <Typography variant="body2" fontWeight={700} noWrap title={label} sx={{ color: 'primary.main' }}>
              {label}
            </Typography>
          </Box>
        </Box>
      )}

      <Stack direction="row" spacing={2} alignItems="center" sx={{ mt: 1 }}>
        <Button
          size="small"
          color={liked ? 'primary' : 'inherit'}
          startIcon={liked ? <ThumbUpIcon fontSize="small" /> : <ThumbUpOutlinedIcon fontSize="small" />}
          onClick={() => void toggleLike()}
          sx={{ color: liked ? 'primary.main' : 'text.secondary', textTransform: 'none' }}
        >
          {likeCount > 0 ? likeCount : '赞'}
        </Button>
        <Button
          size="small"
          startIcon={<ChatBubbleOutlineIcon fontSize="small" />}
          onClick={toggleComments}
          sx={{ color: commentsOpen ? 'primary.main' : 'text.secondary', textTransform: 'none' }}
        >
          {commentCount > 0 ? commentCount : '评论'}
        </Button>
      </Stack>

      {commentsOpen && <CommentThread postId={post.id} onCountChange={onCountChange} />}
    </Card>
  )
})

export default function Community() {
  const [params, setParams] = useSearchParams()
  const page = Math.max(1, Number(params.get('page') ?? '1') || 1)
  const navigate = useNavigate()
  const { user } = useAuth()
  const { toast } = useToast()
  const [dialogOpen, setDialogOpen] = useState(false)

  const state = useAsync(
    async () => communityApi.list(page, PAGE_SIZE),
    [page],
  )

  useEffect(() => {
    window.scrollTo({ top: 0 })
  }, [page])

  const data: RecommendationList | null | undefined = state.data
  const items = data?.items ?? []
  const totalPages = data ? Math.max(1, Math.ceil(data.total / (data.page_size || PAGE_SIZE))) : 1

  const needLogin = useCallback(() => {
    toast('登录后才能点赞和评论', 'warning')
    navigate('/login')
  }, [navigate, toast])

  const openPost = () => {
    if (!user) {
      navigate('/login')
      return
    }
    setDialogOpen(true)
  }

  const remove = async (id: string) => {
    if (!window.confirm('确定删除这条帖子吗？')) return
    try {
      await communityApi.remove(id)
      toast('已删除')
      state.reload()
    } catch (e) {
      toast(e instanceof Error ? e.message : '删除失败', 'error')
    }
  }

  return (
    <Box>
      <Stack direction="row" alignItems="center" justifyContent="space-between" mb={1.5} flexWrap="wrap" useFlexGap>
        <Box sx={{ minWidth: 0 }}>
          <Typography variant="h5" fontWeight={800} sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
            <ForumOutlinedIcon color="primary" /> 社区
            {data?.is_moderator && <Chip label="管理员" size="small" color="primary" variant="outlined" />}
          </Typography>
          <Typography variant="caption" color="text.secondary">
            {data ? `${data.total} 条帖子` : '说说 · 点赞 · 评论'}
          </Typography>
        </Box>
        <Button variant="contained" startIcon={<AddIcon />} onClick={openPost} sx={{ borderRadius: 3, fontWeight: 700 }}>
          发帖
        </Button>
      </Stack>

      {state.loading ? (
        <CenterLoading />
      ) : state.error ? (
        <ErrorState message={state.error} onRetry={state.reload} />
      ) : items.length === 0 ? (
        <EmptyState
          text="还没有人发帖，来发第一条说说吧"
          icon={<ForumOutlinedIcon />}
          action={
            <Button variant="contained" startIcon={<AddIcon />} onClick={openPost} sx={{ borderRadius: 3 }}>
              发帖
            </Button>
          }
        />
      ) : (
        <>
          <Stack spacing={1.25}>
            {items.map((p) => (
              <CommunityPost key={p.id} post={p} onDelete={(id) => void remove(id)} onNeedLogin={needLogin} />
            ))}
          </Stack>
          {totalPages > 1 && (
            <Box sx={{ display: 'flex', justifyContent: 'center', mt: 4 }}>
              <Pagination
                count={totalPages}
                page={page}
                onChange={(_, p) => setParams(p > 1 ? { page: String(p) } : {})}
                color="primary"
              />
            </Box>
          )}
        </>
      )}

      <RecommendDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        onPosted={() => {
          setParams({})
          state.reload()
        }}
      />
    </Box>
  )
}