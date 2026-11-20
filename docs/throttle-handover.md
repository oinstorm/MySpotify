# Spotify 限流优化交接文档

## 2026-11-20：证书缓存与请求统计（仅第 1 步）

- 每个本地播放页只在内存缓存当前上游的应用证书，最多 24 小时，遵守更短的上游有效期及禁止缓存指令。不会缓存失败、空、截断、超大或明显不是证书的响应；同时到达的可缓存请求共用一次成功获取。
- `/cert` 日志用 `source=upstream` 标明实际上游尝试，用 `source=cache` 标明本地命中；按 `track` 和 `reason` 与原 `/license` 日志对照。日志不包含证书、令牌、请求体或响应体。轨道编号在播放页重建后可能从头计数。
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
