// 构建后置步骤：为静态资源生成 .br / .gz 预压缩兄弟文件。
//
// 之前所有文本资源都由服务端 gzip 中间件在「请求时」现场压缩：
// 这台机器只有 2 核，上百人同时访问时压缩 CPU 会成为瓶颈，
// 而且 gzip 的压缩率也不是最优。改成构建期压好、运行时直接下发，
// 运行时零 CPU，同时可以用上压缩率更好的 brotli。
//
// 兼容性：
//   - 后端优先下发 .br，但只在 HTTPS 下发（Safari 只在安全上下文启用 br）；
//     明文连接一律走 .gz，再不行回落到原始文件。
//   - 任何一步失败都不影响构建产物本身。
import { readdir, readFile, writeFile, stat, rm } from 'node:fs/promises'
import { join, extname, relative } from 'node:path'
import { promisify } from 'node:util'
import zlib from 'node:zlib'

const gzip = promisify(zlib.gzip)
const brotliCompress = promisify(zlib.brotliCompress)

const OUT_DIR = process.argv[2] || '../server/internal/app/webdist'

// 只压文本类资源：图片、音视频本身已是压缩格式，再压一遍纯属浪费
const COMPRESSIBLE = new Set([
  '.js', '.mjs', '.css', '.html', '.json', '.map', '.txt', '.xml', '.webmanifest', '.svg',
])

async function walk(dir) {
  const out = []
  let entries
  try {
    entries = await readdir(dir, { withFileTypes: true })
  } catch {
    return out
  }
  for (const e of entries) {
    const p = join(dir, e.name)
    if (e.isDirectory()) out.push(...(await walk(p)))
    else if (e.isFile()) out.push(p)
  }
  return out
}

function fmt(n) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(2)} MB`
}

async function main() {
  const files = await walk(OUT_DIR)
  if (files.length === 0) {
    console.warn(`[precompress] ${OUT_DIR} 里没有文件，跳过`)
    return
  }

  let rawTotal = 0
  let gzTotal = 0
  let brTotal = 0
  let count = 0
  const produced = new Set()

  for (const file of files) {
    // 已经是压缩产物，跳过（否则会生成 .gz.gz）
    if (file.endsWith('.br') || file.endsWith('.gz')) continue
    const ext = extname(file).toLowerCase()
    if (!COMPRESSIBLE.has(ext)) continue

    const data = await readFile(file)
    if (data.length === 0) continue

    const [gz, br] = await Promise.all([
      gzip(data, { level: 9 }),
      brotliCompress(data, {
        params: {
          [zlib.constants.BROTLI_PARAM_QUALITY]: 11,
          [zlib.constants.BROTLI_PARAM_SIZE_HINT]: data.length,
          [zlib.constants.BROTLI_PARAM_MODE]: zlib.constants.BROTLI_MODE_TEXT,
        },
      }),
    ])

    // 压完反而更大就别写了（比如本来就压不动的小文件）
    if (gz.length < data.length) {
      await writeFile(file + '.gz', gz)
      produced.add(file + '.gz')
      gzTotal += gz.length
    }
    if (br.length < data.length) {
      await writeFile(file + '.br', br)
      produced.add(file + '.br')
      brTotal += br.length
    }
    rawTotal += data.length
    count++
  }

  // 清掉源文件已经不存在的旧压缩产物（避免打进二进制里变成死资源）
  for (const file of files) {
    if (!file.endsWith('.br') && !file.endsWith('.gz')) continue
    if (produced.has(file)) continue
    const src = file.replace(/\.(br|gz)$/, '')
    try {
      await stat(src)
    } catch {
      await rm(file, { force: true })
      console.log(`[precompress] 清理过期产物 ${relative(OUT_DIR, file)}`)
    }
  }

  console.log(
    `[precompress] ${count} 个文本资源：原始 ${fmt(rawTotal)} → gzip ${fmt(gzTotal)} / brotli ${fmt(brTotal)}`,
  )
}

main().catch((err) => {
  // 预压缩只是锦上添花，失败不该让整个构建挂掉
  console.warn('[precompress] 跳过：', err?.message || err)
})