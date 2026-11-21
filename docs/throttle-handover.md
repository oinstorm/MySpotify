# Spotify 限流优化交接文档

## 2026-11-21：减少失败重载的授权消耗

- 起因：18:26–18:47 一轮里 14 次授权成功、0 首播完，同时 `audio4-ak` 返回 EOF、预取不足。授权按“每次加载”消耗，媒体故障会放大消耗，而不是按播完一首消耗。
- 现在媒体或网络失败先由同一个播放对象重载，最多两次（`vinyl.retry`），期间不解决媒体就复用原授权；两次后仍失败才跳下一首。授权被拒（429）和视频失败仍走原有路径，不进入重试。
- 授权日志补上原因：`choose`（手动选歌）、`advance`（自然换曲）、`next`（手动下一首）、`after-failed`（失败后跳过）、`throttle-retry`、`connect`、`radio-feed`、`resume`、`load`。被拒时另记 `requests in last 5m=N 15m=N wait=… from=retry-after|local-backoff`。
- 审计：重试路径经离线计数确认产生 0 次新授权；失败三次后跳曲为 +1；同一播放对象两次有效挑战仍各自请求。`licence blocked locally` 仍单列，不计入上游尝试。
- 仍不能消除 Spotify 在必要授权上返回的 429；这一版只降低媒体故障带来的额外授权，并让“为什么发这次请求”在日志里可区分。


## 2026-11-21：排除本机多发授权

对有结构化 `/license track=` 的历史样本（截至 17:43 最后一条日志）单独统计上游尝试：160 条，均为 `attempt=1`。自然换曲 127 次成功、7 次 429；自动冷却重试 1 次成功、14 次 429。按启动、编号回绕和播放对象分组未见同一对象重复请求；这是已覆盖样本的观测，不是对所有操作或服务器计数维度的证明。

本地模拟服务重放：首个授权拒绝后页面尚未报错，8 个并发请求旧版实际发了 8 次，修复后只发 1 次；切歌后立即到达的旧曲请求从 1 次降为 0 次。音乐与 MV 共用同一授权冷却；401 换身份重发前重新检查对象是否仍有效。正常的不同授权挑战仍可分别完成，不缓存或跨请求复用授权正文。原退避档位和用户暂停行为不变。

实机用 `player: /license track=...` 统计实际出站尝试，`player: licence blocked locally ...` 另计为本地拦截；不要把重复的状态说明、证书请求或授权重试算成独立的首次限流。真实上游 429 即使出现在一次必要请求上也仍可能发生。

## 2026-11-21：修复真实响应中的 `Vary: Origin` 缓存零命中

- 实机诊断确认：同一 `page=1` 的证书响应均为 `Cache-Control: public, max-age=86400` 与 `Vary: Origin`，但旧版将除 `Accept-Encoding` 外的所有 Vary 都判为不可缓存，产生 `cache=vary-origin`。这属于本地兼容性遗漏，不是 Spotify 禁止缓存。
- 缓存现在同时匹配上游目标和实际上游 Origin 值；未设置、显式空值、`null` 和不同 Origin 均隔离。当前生产转发不带 Origin，不能用本地 WebView 的 Origin 代替它，也不转发不受信任的入站 Origin。
- 仍仅在内存保存每个 Web 实例的一份证书；尊重 `no-store`、`no-cache`、有效期及不支持的 Vary，`Vary: *` 仍不可复用。不改变 `/license`、播放队列、退避、界面或 Hls.js。
- 修复前先以真实响应头模拟三首换曲复现失败：上游收到三次证书和三次歌曲授权。修复后要求一次证书和三次独立授权，并覆盖 Origin 隔离、并发隔离、Origin 缺失与空值差异，以及页面重建。
- 实机验收目标是首次 `/cert` 记录 `cache=stored-max-age`，随后同一页自然换曲出现 `source=cache`，不再每首 `source=upstream`。这只验证请求减少，不保证 Spotify 不再对歌曲授权返回 429。

### 缓存修复审计与交付（2026-11-21 14:20）

- 应用提交：`ecf52233dfff78f15a81c4b9a9623b3c71052fae`。修复前真实响应回归明确失败，修复后证书专项连续五轮通过；模拟三首歌为一次证书请求、三次独立歌曲授权。
- 本地全量内部测试、`go vet ./...`、`go tool mygo vet .`、播放页 Node 回归通过；macOS arm64 播放包交叉编译通过。
- GitHub Actions `44874667942` 成功，包含 Linux 证书缓存 race 检查连续五轮和 Windows 播放器测试、构建。审计覆盖不同/相同 Origin、缺失/空值/null、并发隔离、不转发本地 Origin，以及保留 no-store/no-cache/不支持 Vary 的限制。
- EXE 产物：`14695681415`；完整包产物：`14695581775`。本地文件：`build/certificate-origin-ecf5223/MySpotify.exe`。
- 已核对 EXE 内嵌修订号及 SHA-256：`33C029DAC28473B8075AB3BF92BC7B02AA93E770A86FB87E1B9BF3443F83DE00`；未启动或覆盖当前程序。

本次修复范围内未发现阻断交付的问题。缓存逻辑和模拟场景通过验收；实际 Spotify 证书缓存命中及对 429 的影响仍需安装后观察，不宣称限流已解除。

## 2026-11-20：证书缓存与请求统计（仅第 1 步）

- 每个本地播放页只在内存缓存当前上游的应用证书，最多 24 小时，遵守更短的上游有效期及禁止缓存指令。不会缓存失败、空、截断、超大或明显不是证书的响应；同时到达的可缓存请求共用一次成功获取。
- `/cert` 日志用 `source=upstream` 标明实际上游尝试，用 `source=cache` 标明本地命中；按 `track` 和 `reason` 与原 `/license` 日志对照。日志不包含证书、令牌、请求体或响应体。轨道编号在播放页重建后可能从头计数。
- 诊断版日志还输出 `page` 实例编号、缓存未命中原因、缓存决定、有效期及响应的 `Cache-Control`、`Vary`、`Age`、`Expires` 元数据；这能区分上游禁止缓存与播放页重建。不得输出证书本体或身份信息。
- 新增回归检查默认及显式 HTTP 缓存期限、`no-store`/`no-cache`/`Vary`/`Age`/`Expires` 的诊断原因、缓存命中不泄漏证书字节，以及播放页实例隔离。Windows 云构建前增加 Linux Go race detector 重复运行证书缓存用例。

### 代码核验构建（2026-11-20 22:07）

- 当前代码提交：`2ef808d860cbb46a87144d979c4ce187c11df700`；云构建 `44749585967` 成功，Linux race 检查、Windows 测试和构建均成功。
- 新构建首次上游响应日志会带：`page`、`track`、`reason`、`attempt`、本地 `status`、上游 `upstream_status`、`miss`、`cache`、`ttl` 与缓存策略相关响应头。
- cache miss 分类包括空缓存、缓存过期、目标变化、身份/传输/响应错误、`no-store`/`no-cache`、无效有效期以及具体 `Vary` 字段。
- 新 EXE：`build/certificate-diagnostics-2ef808d/MySpotify.exe`；SHA-256：`801B2B1DAF129CAF5B1E903E4C9B3A559BE7D8A93558A4315E488F2DB6994B17`；已验证内嵌修订号和 SHA-256，未启动。
- 本地 `go test ./internal/...`、证书专项 `-count=5`、`go vet ./...`、`go tool mygo vet .` 和播放页 Node 测试通过。本机缺 GCC，未能运行本地 Go race detector；Linux 云端 `go test -race -count=5 ./internal/player -run '^TestCertificate'` 成功。

本次是诊断构建，不改变缓存规则或限流/重试策略。安装并完全退出旧版后，正常顺序播放几首，查看每个 `page` 的首条 `/cert` 上游日志中的 `cache=` 与 `cache_control` / `vary` / `age` / `expires`，即可确认缓存为何未命中，以及是否由播放页重建造成。
- Windows 的 Hls 路径原本每次新建播放对象，历史日志未统计成功的 `/cert`，所以不能用旧日志证明固定的每曲证书请求数。macOS 的原生 FairPlay 页面原有证书复用逻辑，保持不变。
- 用正常连播验收：统计 `/cert` 的上游尝试和缓存命中，以及自然换曲的 `/license` 200/429。预期减少 `/cert` 的上游次数，不预设证书与授权共享限额。不调整歌曲授权、退避、播放对象复用或界面行为。

## 2026-11-19：第一批身份稳定优化与审计验收

范围仅限设备身份与设备令牌；不升级 Hls.js，不改变歌曲 license 的加载方式，也不改变上一版的四次拒绝停自动重试策略。

- `device-id` 保存于本机账户凭据目录，重启、退出登录、用户访问令牌续期都不重生成；设备令牌请求使用当前操作系统及版本。
- `device-credentials` 沿用账户凭据的系统加密和原子写入方式；缓存绑定设备、client ID、客户端版本和平台，过期或不匹配的缓存不复用。
- 根据 `refresh_after_seconds` 和 `expires_after_seconds` 分离刷新时间与硬过期时间；刷新失败仅复用未过期令牌，并遵守重试等待，避免每个调用方重复刷新。未知有效期的响应不做长期缓存。
- 同一设备的请求串行合并；日志记录缓存恢复、刷新、回退和存储状态，不记录设备 ID、令牌或响应正文，也不在每个正常请求中刷重复日志。
- 审计边界包括：损坏文件不覆盖设备 ID、加密失败不落明文、跨 client ID/平台/版本不串用、并发刷新只发一次、刷新失败后拒绝过期凭据，以及慢响应到达时凭据已经过期。
- 审计修正：慢响应可能在返回时已经超过令牌有效期；现在返回前再次检查硬过期时间，并覆盖零、负数和溢出有效期，拒绝已知无效的响应。
- 审计修正：用户访问令牌尚有效时，设备令牌也可能独立到期。取得请求身份时会单独检查设备令牌，不续期仍有效的用户访问令牌；设备凭据过期且刷新失败时，不再把旧值带出。匿名浏览仍允许不带设备令牌。
- 本地验收：`go test ./internal/...`、新回归 `-count=5`、`go vet ./...`、`go tool mygo vet .`、播放页 Node 回归；云端增加 Linux 竞态检测及 Windows 持久化回归，Windows 构建依赖竞态检查通过。

### 最终验收结果（2026-11-19 21:58）

- 应用代码提交：`dd78e3425edebfca22956e48c556e5e8ce35b0ff`；第一批实施提交为 `41f2286`。
- 本地全量内部测试、目标回归连续五次、`go vet ./...`、`go tool mygo vet .`、播放页 Node 回归均通过；macOS Spotify 包交叉编译通过。
- 最终 GitHub Actions：`44537827222`，`device-identity-audit` 和 `test-and-build` 两个作业均 `success`；包括 Linux 竞态回归五次与 Windows 持久化回归五次。
- EXE 产物 ID：`14536269396`；完整包产物 ID：`14537321361`。已下载 EXE 并核对内嵌代码提交及 SHA-256。
- EXE SHA-256：`589E9EEB4BE0D6D61A0F4AF1531A88EB660D6278F767833194DBAC67190D6A25`。
- 本地 EXE：`build/device-identity-dd78e34/MySpotify.exe`；未启动、未替换当前运行程序。

代码与自动化验收未发现阻断本次交付的问题。系统加密不可用时仅在内存复用设备令牌，不落明文；磁盘读写故障会记录日志，不能据此认为跨重启持久化已经成功。本次验收不证明 Spotify 已解除当前账号的许可证 429；实机连续播放效果仍需安装此构建后确认。

## 2026-11-19：顺序播放再次限流

实机日志显示，单曲循环复用当前播放对象；顺序换曲仍可能收到 `/license` 429。出现过多次「播完一首，下一首马上 429，等待又从 30 秒开始」；这不能证明 Spotify 的配额按歌曲数计算。

后续实现替换了下文基准版本的「播满即恢复」：连续播完三个新获授权的播放对象后，退避下降一档，单曲循环不重复计数。没有新的被拒或新获授权播放完成达 10 分钟，也可回到首档。退避累计四次拒绝后不再自动重试，冷却结束后由用户手动继续。有效的 `Retry-After` 按原值等待。新日志 `player: /license track=… reason=… attempt=… status=…` 包括成功响应，可按播放对象区分自然换曲与自动重试；不含授权内容。

适用范围：MySpotify 播放器（`internal/player`）与播放栏（`internal/app/ui_player.go`）。
基准版本：`c32b606`（行号以此为准，后续改动可能偏移）。
状态：阶段 1、2、3 已合入 `feature/windows-http-proxy` 并通过 GitHub Actions 构建；实机效果尚未采集数据确认。

---

## 1. 一句话摘要

Spotify 的**许可证接口**（`spclient` 的 license 请求）按**设备身份**限流，返回 429。原来的实现每首歌、每次起播都直接申请许可证，被拒后只做一次固定等待，期间用户切歌、拖动、Connect 遥控又各自发起请求，形成"越点越撞"的连锁。本次改动引入**许可证闸门**（冷却期内不再发请求）、**分级退避**、**401 抖动**，并把等待可视化，另外减少了页面重建与重复 seek。

---

## 2. 问题现象

- 播放中不定期提示「Spotify 暂时限流，稍后重试」，时段不固定，与本地高峰无关。
- 正常一首 3–4 分钟的歌也会触发，不需要频繁操作。
- 一旦出现，切到下一首仍然限流，像是"跟着设备走"。
- 缓存再大也无效（缓存只存加密分段，许可证每次都要重新申请）。

---

## 3. 结论与外部证据

| 结论 | 依据 |
|---|---|
| 限流对象是**设备/账号身份**，不是单曲，所以会跨歌曲延续 | 429 在 `web.go` 的 `/license` 转发处返回；等待后重载的是同一首，仍被拒 |
| 官方与第三方都可能被限流，第三方更易触发 | 官方端合并与串行请求；第三方普遍按需即时请求 |
| 社区确认"被拒后立即重试会延长限流" | librespot issue #1649、PR #1844（音频密钥接口，机制同类）：被拒后 3/5/10 秒退避、请求串行且间隔 ≥200 ms，成功才重置 |
| 属于部分账号、按突发程度触发，非所有用户必然出现 | 上述 issue 原文 "Some accounts get runs of refusals" |

差异提示：librespot 走音频密钥接口，返回无状态码的拒绝；本项目走 WebView 许可证接口，返回标准 `429 + Retry-After`。**退避思路可借鉴，具体分支不能照搬。**

社区链接（外部）：

- https://github.com/librespot-org/librespot/issues/1649
- https://github.com/librespot-org/librespot/pull/1844

---

## 4. 已合入的改动

| 提交 | 内容 |
|---|---|
| `d90a69c` | Connect 队列不再被拖动/音量事件打满；远程 seek/音量合并；上一首/下一首正确路由；长歌词跳转定位 |
| `3d8743f` | 随机与循环互斥（开启随机关闭循环，反之亦然） |
| `f433270` | **阶段 1**：许可证闸门、分级退避、401 抖动、日志 |
| `c32b606` | **阶段 2+3**：播放栏倒计时、播放页保活 20 分钟、Connect 重复状态不再 seek |

---

## 5. 设计细节

### 5.1 许可证闸门（`licenceGate`）

状态：

| 字段 | 含义 |
|---|---|
| `until` | 在此之前不申请许可证 |
| `fails` | 连续被拒次数，决定退避档位 |
| `last` | 最后一次被拒时间，用于安静衰减 |
| `played` | 某首歌开始播放的时间，用于"播满即恢复" |

规则：

- `refused(now, wait)`：若离上次被拒超过 `gateQuiet`（10 分钟）先清零 `fails`；`wait<=0` 时按 `30s << min(fails,4)` 计算；整体上限 30 分钟；`until` 只延长不缩短。
- `shut(now)` / `left(now)`：是否还在冷却、还剩多久。
- `open()`：等待结束，允许再次申请（`fails` 保留，用于继续分级）。
- `started(now)`：曲目开始播放时记录时间（仅当 `fails>0`）。
- `playedFor(now)`：播放满 `gateGood`（30 秒）后把 `fails` 归零，退避回到首档。

退避实际序列（`Retry-After` 缺省时）：30s → 60s → 120s → 240s → 480s → 480s…（`<<4` 封顶）。

### 5.2 起播拦截（`gated`）

`loadSkipping` 入口先调用 `gated(at, skipped, play, from)`：

- 闸门开 → 正常加载。
- 闸门关 → 把目标（`at`/`skipped`/`play`/`from`）放进到期回调的闭包，`pending` 保存续播位置，**不发请求**，`sync()` 更新界面。
- 已有等待在跑 → 直接返回，避免重复排队（"一次限流只消耗一次配额"）。
- 到期回调：`waiting=false` → `licence.open()` → 若仍在播放则 `loadSkipping` 重试一次。

### 5.3 429 与 401 分支

- 429：`failed()` → `throttled(wait)`，写日志、进入闸门、安排到期重载。
- 403：停在当前曲（`这首歌暂时无法播放`），不连锁尝试队列其余曲目。
- 401：`web.go` 的 `/license` 转发换身份重发，**重发前停 1 秒 + 0–999 ms 随机抖动**，避免同一瞬间两次请求被计为突发。

### 5.4 播放页保活

- `idleAfter = 20 * time.Minute`（原 5 分钟）。
- 语义不变：长时间暂停仍会释放页面（既有测试 `TestIdleReleaseAndResume` 固定该行为，未改动）。
- 目的：减少"暂停久了再播"重新申请许可证的次数。

### 5.5 Connect 去重

`ApplyConnect` 在同曲目、非远程输出时：

- 位置变化 < 2 秒且播放状态一致 → 直接返回，不 seek、不 toggle。
- 否则照旧 seek / toggle。

### 5.6 倒计时提示

- `Player.Limiting()` 返回剩余等待，0 表示未限流。
- 播放栏在已有错误文案分支里替换为「Spotify 限流，约 N 秒后自动继续」，并 `c.After(time.Second)` 触发每秒重绘。

---

## 6. 代码索引

| 文件:行 | 内容 |
|---|---|
| `internal/player/limiter.go:13` | `licenceGate` 定义 |
| `internal/player/limiter.go:22` | `gateQuiet = 10 * time.Minute` |
| `internal/player/limiter.go:26` | `gateGood = 30 * time.Second` |
| `internal/player/player.go:278` | `throttled`（429 入口） |
| `internal/player/player.go:306` | `gated`（起播拦截） |
| `internal/player/player.go:627` | `Limiting()` |
| `internal/player/player.go:910` | `idleAfter = 20 * time.Minute` |
| `internal/player/player.go:912` | `updateIdle` |
| `internal/player/player.go:974` | `loadSkipping` 入口调用 `gated` |
| `internal/player/web.go:467` | 401 换身份日志与抖动静默等待 |
| `internal/player/connect.go:89` | `ApplyConnect` 与 2 秒去重 |
| `internal/app/ui_player.go:389` | 播放栏倒计时文案 |

---

## 7. 日志与验证

日志文件：`%APPDATA%\Vinyl\vinyl.log`（超过 1 MB 轮转为 `vinyl.log.1`）。

### 7.1 关键日志行

| 日志 | 含义 |
|---|---|
| `player: /license: 429 Too Many Requests, retry after 7s: ` | 许可证被拒 |
| `player: throttled, again in 7s` | 进入冷却，记录本次等待 |
| `player: throttled, waiting 12s before asking again` | 冷却期间又有起播被闸门拦下（**说明机制生效**） |
| `player: throttled, asking again` | 等待结束，重试一次 |
| `player: /license: 401, renewing the identity` | 身份过期换新（其后 1–2 秒才出现下一次请求） |

### 7.2 聚合统计

```powershell
$log = "$env:APPDATA\Vinyl\vinyl.log"
$lines = Get-Content $log
[pscustomobject]@{
  '限流次数(429)' = ($lines | Select-String ' /license: 429').Count
  '等待时长行'     = ($lines | Select-String 'throttled, again in').Count
  '被闸门拦下'     = ($lines | Select-String 'before asking again').Count
  '等待后重试'     = ($lines | Select-String 'throttled, asking again').Count
  '身份换新(401)'  = ($lines | Select-String 'renewing the identity').Count
} | Format-List
```

判读：

- `等待后重试 ≈ 限流次数`，且 `被闸门拦下 > 0` → 闸门有效。
- `等待后重试` 明显多于 `限流次数` → 闸门有漏洞，优先查 `gated` 的 `waiting` 复用分支。
- 相邻两次 `again in` 的数值应递增，而不是反复 30 秒 → 检查 `fails` 是否被意外清零。

### 7.3 时间线

```powershell
Select-String -Path $log -Pattern 'throttled|/license: (429|401)' |
  Select-Object -Last 40 | ForEach-Object { $_.Line }
```

### 7.4 A/B 对照（推荐）

同一天交替跑，各 30–60 分钟，比「429 次数/小时」：

| 版本 | 构建产物 |
|---|---|
| 基线 `d90a69c` | https://github.com/oinstorm/MySpotify/actions/runs/41380812275/artifacts/13112849236 |
| 阶段 1 `f433270` | https://github.com/oinstorm/MySpotify/actions/runs/44475510518/artifacts/14506700705 |
| 阶段 1+2+3 `c32b606` | https://github.com/oinstorm/MySpotify/actions/runs/44480497123/artifacts/14507684707 |

注意：不要跨天比较（配额窗口与账号状态随时间漂移）；换版本前先退出程序；记下每次开始的时刻，便于从日志切片。

### 7.5 确定性构造（可选）

- 本地 MITM 代理：让 MySpotify 的 HTTP 代理指向本地代理，仅对 `spclient.wg.spotify.com` 的许可证路径返回 `429 + Retry-After: 5`，其余放行。
- 调试开关（**尚未实现**）：在许可证转发处按环境变量直接返回 429，例如 `NM_THROTTLE=5s`。需新增代码，默认关闭；若实现，必须同时补一条 CI 用例，避免只在本地有效。

---

## 8. 测试覆盖

| 用例 | 位置 | 验证内容 |
|---|---|---|
| `TestGateWaitsAsAsked` | `internal/player/limiter_test.go:11` | 按 `Retry-After` 冷却，期内 `shut` 为真 |
| `TestGateBacksOffFurther` | `limiter_test.go:28` | 缺省等待逐级递增至封顶 |
| `TestGateForgetsAfterAQuietSpell` | `limiter_test.go:46` | 安静 10 分钟后回到 30 秒档 |
| `TestGateLetsGoAfterPlaying` | `limiter_test.go:60` | 播满 30 秒后 `fails` 归零 |
| `TestThrottledCountsDown` | `internal/player/fault_test.go:170` | 限流期间 `Limiting()>0`，等待结束后为 0 |
| `TestConnectSameStateDoesNotSeek` | `internal/player/connect_test.go:131` | 重复状态不 seek，位置真变才 seek |

既有回归（未改断言，全部通过）：`TestThrottledLicenceWaits`、`TestThrottledMediaWaits`、`TestThrottledPausedStays`、`TestRefusedLicenceStops`、`TestFailedTrackSkips`、`TestLicenceRenewsIdentity`、`TestIdleReleaseAndResume`、`TestIdleTimeoutCancelled`。

本地执行（Windows，Go 1.27.3）：

```powershell
go vet ./...
go test ./internal/player ./internal/app ./internal/spotify ./internal/lyrics
```

最近一次结果：`vet` 通过；player 4.0s、app 35.3s、spotify、lyrics 均 `ok`。云端 run 44480497123 全步骤通过。

---

## 9. 已知限制与未做项

1. **无法从程序内主动触发 429**，实机验证只能等限流或搭 MITM 代理。
2. **页面保活只延长到 20 分钟**，暂停更久仍会释放页面并重新申请许可证（既有测试固定该行为）。
3. **页面重建时未复用旧输出**；`Web.Open` 目前每次新建 `webOut`。
4. **倒计时精度**为秒级，且只在播放栏可见；通知区与 Connect 侧不显示剩余时间。
5. **Remote Connect 场景下**，限流闸门只作用于本机申请许可证的路径；远端设备自身的限流无法干预。
6. **未做多账号或身份轮换**（违反条款，明确排除）。
7. CI 里仍保留一条早期诊断步骤（`App long transcript test` 写 `long-transcript.log` 并上传），与本改动无关，可按需删除。

---

## 10. 交接清单

- 分支：`feature/windows-http-proxy`（已推送至 `c32b606`）。
- 构建：`.github/workflows/windows-build.yml`，push 即触发；产物为 `MySpotify-windows-amd64-exe-<sha>`。
- 数据位置：设置与日志在 `%APPDATA%\Vinyl`；分段缓存在 `%LOCALAPPDATA%\Vinyl\Cache\tracks`（大小上限可在设置里改）。
- 凭据：本仓库推送使用 GitHub PAT（`repo` 权限；若有工作流改动还需 `workflow`）。**不要写入仓库或脚本**；本次会话使用的令牌已明文出现在对话里，应交由账号所有者撤销后重新签发。
- 版本记录：改动写入 `CHANGELOG.md` 的「未发布」段；发版时 `mygo build` 会读取对应版本小节。

---

## 11. 后续建议（按性价比）

1. **采集数据**：按 7.4 做一次同一天 A/B，确认 429/小时是否下降；没有数据前不要继续加机制。
2. **若仍有连锁**：检查 `gated` 的 `waiting` 复用分支是否被 `cue()` 重置（`cue` 会把 `waiting` 置 false），必要时把"待加载目标"从 `pending` 拆成独立字段。
3. **页面复用**：让同一曲目在页面重建时复用既有的 `webOut`，减少一次许可证申请（需先确认 `Web.Open` 的幂等性）。
4. **调试开关**：实现 `NM_THROTTLE`，并补 CI 用例，使闸门可被稳定回归。
5. **提示一致性**：把倒计时也反映到系统媒体信息与 Connect 状态，避免用户在多处看到不同步的信息。
