import { useEffect, useMemo, useState } from 'react'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogContent from '@mui/material/DialogContent'
import DialogTitle from '@mui/material/DialogTitle'
import IconButton from '@mui/material/IconButton'
import Stack from '@mui/material/Stack'
import TextField from '@mui/material/TextField'
import ToggleButton from '@mui/material/ToggleButton'
import ToggleButtonGroup from '@mui/material/ToggleButtonGroup'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import CloseIcon from '@mui/icons-material/Close'
import { ApiError } from '../api'
import { POST_BODY_MAX, POST_IMAGES_MAX, communityApi, parseImageLinks } from '../community'
import { useToast } from '../toast'
import type { SourceKind } from '../source'
import type { WorkKind } from '../types'

export const RECOMMEND_BODY_MAX = POST_BODY_MAX

/** 发帖对话框：说说式正文 + 可选关联作品（漫画 / 小说）+ 可选外链图片。
 *
 *  作品 ID 留空即发一条纯说说；由详情页打开时预填作品 ID，保持原来的「漫画推荐」用法。
 *  只支持引用图片链接，不支持上传：服务端不会接收也不会保存任何图片文件。 */
export function RecommendDialog({
  open,
  onClose,
  defaultComicId = '',
  defaultKind = 'comic',
  source = 'jm',
  onPosted,
}: {
  open: boolean
  onClose: () => void
  defaultComicId?: string
  defaultKind?: WorkKind
  /** 作品所属源：哔咔没有小说，类型固定为漫画。 */
  source?: SourceKind
  onPosted?: () => void
}) {
  const { toast } = useToast()
  const isBika = source === 'bika'
  const [comicId, setComicId] = useState(defaultComicId)
  const [kind, setKind] = useState<WorkKind>(defaultKind)
  const [body, setBody] = useState('')
  const [imageText, setImageText] = useState('')
  const [saving, setSaving] = useState(false)

  const images = useMemo(() => parseImageLinks(imageText), [imageText])

  useEffect(() => {
    if (open) {
      setComicId(defaultComicId)
      setKind(isBika ? 'comic' : defaultKind)
    }
  }, [open, defaultComicId, defaultKind, isBika])

  const dropImage = (link: string) => {
    setImageText((prev) =>
      prev
        .split('\n')
        .filter((line) => line.trim() !== link)
        .join('\n'),
    )
  }

  const submit = async () => {
    const cid = comicId.trim()
    if (cid) {
      const idOk = isBika ? /^[0-9a-fA-F]{6,32}$/.test(cid) : /^\d{1,12}$/.test(cid)
      if (!idOk) {
        toast(isBika ? '请输入正确的哔咔作品 ID' : '请输入正确的作品 ID（纯数字）', 'warning')
        return
      }
    }
    if (!body.trim() && images.length === 0) {
      toast('写点什么，或引用至少一张图片', 'warning')
      return
    }
    setSaving(true)
    try {
      await communityApi.create({
        comic_id: cid,
        kind,
        source,
        body: body.trim(),
        images,
      })
      toast('发布成功')
      setBody('')
      setImageText('')
      setComicId('')
      onClose()
      onPosted?.()
    } catch (e) {
      toast(e instanceof ApiError ? e.message : '发布失败', 'error')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onClose={saving ? undefined : onClose} fullWidth maxWidth="sm">
      <DialogTitle sx={{ fontWeight: 700 }}>发布到社区</DialogTitle>
      <DialogContent>
        <Stack spacing={2} sx={{ mt: 0.5 }}>
          <TextField
            label="正文"
            value={body}
            onChange={(e) => setBody(e.target.value)}
            placeholder="说点什么…（支持换行）"
            multiline
            minRows={4}
            maxRows={10}
            fullWidth
            disabled={saving}
            slotProps={{ htmlInput: { maxLength: RECOMMEND_BODY_MAX } }}
            helperText={`${body.length}/${RECOMMEND_BODY_MAX}`}
          />

          <TextField
            label={`图片链接（可选）`}
            value={imageText}
            onChange={(e) => setImageText(e.target.value)}
            placeholder={'每行一个 https:// 图片直链'}
            multiline
            minRows={2}
            maxRows={5}
            fullWidth
            disabled={saving}
            helperText={`不支持上传图片，只能引用图片链接；最多 ${POST_IMAGES_MAX} 张，已识别 ${images.length} 张`}
          />

          {images.length > 0 && (
            <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1 }}>
              {images.map((link) => (
                <Box key={link} sx={{ position: 'relative', width: 72, height: 72 }}>
                  <Box
                    component="img"
                    src={link}
                    alt=""
                    loading="lazy"
                    decoding="async"
                    sx={{
                      width: '100%',
                      height: '100%',
                      objectFit: 'cover',
                      borderRadius: 1.5,
                      bgcolor: 'action.hover',
                    }}
                  />
                  <Tooltip title="移除">
                    <IconButton
                      size="small"
                      disabled={saving}
                      onClick={() => dropImage(link)}
                      sx={{
                        position: 'absolute',
                        top: -8,
                        right: -8,
                        bgcolor: 'background.paper',
                        boxShadow: 1,
                        '&:hover': { bgcolor: 'background.paper' },
                      }}
                    >
                      <CloseIcon sx={{ fontSize: 14 }} />
                    </IconButton>
                  </Tooltip>
                </Box>
              ))}
            </Box>
          )}

          <Box>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              关联作品（可选）
            </Typography>
            {!isBika && (
              <ToggleButtonGroup
                exclusive
                fullWidth
                size="small"
                value={kind}
                disabled={saving}
                onChange={(_, v: WorkKind | null) => {
                  if (v) setKind(v)
                }}
                sx={{ mb: 1 }}
              >
                <ToggleButton value="comic">漫画</ToggleButton>
                <ToggleButton value="novel">小说</ToggleButton>
              </ToggleButtonGroup>
            )}
            <TextField
              label="作品 ID"
              value={comicId}
              onChange={(e) => setComicId(e.target.value)}
              placeholder="留空即纯说说"
              size="small"
              fullWidth
              disabled={saving}
              helperText={isBika ? '填入哔咔作品 ID（24 位十六进制），留空即纯说说' : '填入 JM 链接中的数字 ID，留空即纯说说'}
            />
          </Box>
        </Stack>
      </DialogContent>
      <DialogActions sx={{ px: 3, pb: 2 }}>
        <Button onClick={onClose} disabled={saving}>
          取消
        </Button>
        <Button variant="contained" onClick={() => void submit()} disabled={saving}>
          {saving ? '发布中…' : '发布'}
        </Button>
      </DialogActions>
    </Dialog>
  )
}