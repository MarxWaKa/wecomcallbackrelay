# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目状态

项目已按需求文档 `企业微信通讯录回调转发服务.md`（v3.0）实现，采用**轻量自研管理后台**而非完整 gin-vue-admin 脚手架（用户已确认）。需求文档仍是业务规则的权威来源；`README.md` 记录了部署方式、API、协议细节与实现差异。

- 后端：`server/`（Go 1.27 + Gin + GORM + MySQL 8.4）
- 前端：`web/`（Vue3 + Vite + Element Plus + Pinia）
- 编排：`docker-compose.yml`、`init.sql`、`.env.example`

## 常用命令

后端（在 `server/` 下）：

```bash
go test ./...                                # 全部单元 + 集成测试
go test ./internal/wecom -v                  # 企微加解密 / XML 映射
go test ./internal/relay -v                  # 双模式投递 / HMAC / 退避
go test ./internal/api -run TestCallback -v  # 回调集成测试（纯 Go SQLite）
go test ./internal/api -run TestCallbackReceive -v
go vet ./...
go build ./...
go run .                                     # 需要可用 MySQL 与下列环境变量
```

前端（在 `web/` 下）：

```bash
npm install
npm run dev      # 5173，已代理 /api /wecom 到 127.0.0.1:8888
npm run build    # 产出 web/dist
```

部署（仓库根目录）：

```bash
cp .env.example .env    # 必须修改 MySQL 密码、JWT_SECRET、CONFIG_ENCRYPT_KEY、ADMIN_PASSWORD
docker compose up -d --build
docker compose logs -f backend
```

后端必需环境变量：`JWT_SECRET`（≥16 字符）、`CONFIG_ENCRYPT_KEY`（≥16 字符）、数据库四项；首次启动还需要 `ADMIN_PASSWORD`（≥8 字符）以创建初始管理员。

## 后端架构

请求有两条互不干扰的路径：

- **企微入口** `/wecom/callback`（`internal/api/callback.go`，无 JWT）
  - GET：校验参数与 ±300s 时间戳 → 读取 `wecom_config`（解密 Token/AESKey）→ `VerifyURL` 解密 echostr → 原样返回明文。
  - POST：解析加密 XML → 验签 → 解密 → 解析事件 → 为每个启用消费端写入一条 `relay_tasks`（`OnConflict DoNothing` 幂等）→ 立即返回 `success`。
  - 验签/解密失败返回 403 且不入库；**明文解析失败返回 200 success 但记 ERROR、不入库**（防止企微重试风暴）。
- **管理 API** `/api/*`（`internal/router/router.go`）
  - 登录在 JWT 之外；其余接口依次经过 `JWTAuth` → `CasbinAuth`（admin 全权限、viewer 只读 GET）。
  - `/api/admin-users` 额外要求 `RequireRole("admin")`。
  - 响应统一为 `{code:0,msg:"ok",data:...}`，错误使用对应 HTTP 状态。

投递链路（`internal/relay/`）：

1. `Worker.claimTasks` 在事务中用 `SELECT ... FOR UPDATE SKIP LOCKED` 把任务置为 `sending`；每批最多 100 条，按 `WORKER_CONCURRENCY` 并发。
2. `processTask` 读取消费端；JSON/wecom_xml 模式解密其认证配置/密钥，构造 `Target`；`wecom_proxy` 模式不读取消费端密钥，直接使用任务里的原始加密快照。
3. `Sender` 按 `task.ForwardMode` 构造请求：JSON 模式附 `X-Event-Id` 与 none/bearer/basic/HMAC；企微兼容模式用消费端密钥 `EncryptMsg` 重新加密 `raw_xml`，并把新的 `msg_signature/timestamp/nonce` 追加到回调 URL；`wecom_proxy` 模式用 `DeliverProxy` 原样转发 `raw_encrypted_body` 与 `inbound_query`，不重新加密。
4. 失败：`retry_count+1`，按 1/2/4/8/16s 退避；`retry_count > max_retry` 时事务内更新任务为 `dead` 并写入 `dead_letters`（含载荷与密钥快照）。
5. 消费端被删除/禁用、配置解密失败等非重试错误直接标 `failed`。
6. 启动时会把超过 5 分钟的 `sending` 任务恢复为 `pending`；关闭时把尚未开始处理的任务放回 `pending`。

数据模型（`internal/model/models.go`）：

- `consumers`：`forward_mode` = `json|wecom_xml`；兼容模式保存加密后的 `wecom_token`、`wecom_encoding_aes_key`、明文 `wecom_corp_id`。
- `wecom_config`：本期单条记录，`secret/token/encoding_aes_key` 加密存储。
- `relay_tasks`：`(event_id, consumer_id)` 唯一索引，快照 `raw_xml` 与 `json_payload`。
- `dead_letters`：保存投递目标、认证与密钥快照；状态 `pending|replaying|resolved|discarded`。
- `admin_users`、`audit_logs`。

## 关键约束与易踩坑点

1. **企微加解密优先用官方库**：`internal/wecom/crypt.go` 的 `NewCrypto` 内部创建 `github.com/sbzhu/weworkapi_golang/wxbizmsgcrypt`（XML 版），生产链路的 `VerifyURL` / `DecryptMsg` / `EncryptMsg` 全部委托官方库；不要改回手写 AES。`Crypto.Signature`、`IsTimestampValid` 仅用于出站签名/诊断/时间窗口。`TestDecryptOfficialTestVector` 使用官方文档 91144 的固定向量，修改后必须跑 `go test ./internal/wecom -v`。
2. **event_id 幂等**：`MakeEventID` 优先用 `evt_<MsgId>`，无 MsgId 时用明文 SHA1；不要改成随机值，否则企微重复投递会产生重复任务。
3. **敏感字段**全部经 `security.Cipher` AES-256-GCM 加密（`enc:v1:` 前缀）。Handler 返回前必须脱敏；更新时 `""` 或 `"******"` 表示保留原值。日志中禁止输出明文密钥与请求体。DEBUG 日志默认只输出明文载荷的长度/哈希；只有 `RELAY_LOG_RAW_EVENTS=true`（默认 false，含 PII）才输出明文 XML/JSON Body。
4. **布尔字段默认值**：`AdminUser.Enabled`、`Consumer.Enabled` 的 GORM tag 故意没有 `default:true`，否则 GORM 会忽略 `false` 的零值，导致无法创建禁用账号/消费端。新增带默认值的字段时注意同类问题。
5. **响应优先**：不要在前台请求路径做任何转发或外部 HTTP 调用；事件只入库，转发由 Worker 完成。
6. **重试语义**：`max_retry=5` 表示最多 5 次重试（初始投递 + 5 次重试），第 6 次失败才转死信。退避由 `RETRY_BACKOFF_SECONDS` 配置（默认 `1,2,4,8,16`），`relay.BackoffFor` 在序列耗尽后使用最后一个值；固定 30 分钟填 `1800`。`relay.Backoff` 是默认策略。
7. **兼容模式密钥**：入站解密用全局 `wecom_config`；出站重加密用**消费端**的 `wecom_token/aes_key/corp_id`。两者在无感切换场景下通常配置为同一套，但代码上相互独立。
8. **AgentID 透传**：入站外层 XML 的 `<AgentID>` 会快照到 `relay_tasks.agent_id` / `dead_letters.agent_id`，兼容模式转发时原样写回外层报文；测试按钮使用消费端可选的 `wecom_agent_id`。修改外层报文构造时保持 `ToUserName → Encrypt → AgentID` 顺序，AgentID 为空也保留空节点。
9. **原样透传模式**：`wecom_proxy` 在入站时把原始加密 Body 与 Query 快照到 `relay_tasks.raw_encrypted_body/inbound_query`，Worker 用 `Sender.DeliverProxy` 原样转发，不重新加密；消费端必须与「企微参数」共享 Token/AESKey/CorpID。死信同样保存这两份快照，缺少快照时重推直接失败。
10. **死信快照**：死信重推使用创建时的 CorpID/Token/AESKey/AgentID 快照，不会自动读取修改后的消费端配置；排查“改了配置仍失败”时先确认快照是否过期。死信支持 `DELETE /api/dead-letters/:id` 与 `POST /api/dead-letters/batch-delete`（≤100）；`replaying` 状态单条删除返回 409、批量删除自动跳过。
11. **多实例**：Worker 的 `SKIP LOCKED` 抢占依赖 MySQL 8.0+；SQLite 仅用于测试，不支持该锁，因此不要给 Worker 写 SQLite 集成测试。
12. **前端令牌键**是 `relay_token` / `relay_user`（`web/src/store/auth.js`）；HTTP 401 时会清理并跳转登录（`web/src/api/http.js`）。
13. **前端敏感字段**：编辑消费端/企微参数时，密码类输入框留空表示不修改；不要用脱敏值回填真实输入框后再提交（会被当成新值）。

## 测试布局

- `internal/wecom/crypt_test.go`：官方库加解密、VerifyURL、EncryptMsg、官方固定测试向量、错误密钥/CorpID、时间戳窗口。
- `internal/wecom/xml_test.go`：成员/部门事件映射、稳定 event_id。
- `internal/relay/sender_test.go`：JSON none/bearer/HMAC、企微兼容模式端到端验签解密、原样透传 Body/Query、非 2xx。
- `internal/relay/worker_wecom_test.go`：企微兼容模式与原样透传模式的 Worker 投递。
- `internal/relay/backoff_test.go`：退避序列。
- `internal/api/callback_test.go`：基于 `github.com/glebarez/sqlite` 的回调集成测试（GET 验证、POST、幂等、403、解析失败、AgentID 透传）。
- `internal/security/cipher_test.go`：AES-GCM 与脱敏。

## 与需求文档的已知差异

- 未引入完整 gin-vue-admin：登录、JWT、Casbin 角色、管理员 CRUD 为轻量实现；无动态菜单/`sys_*` 表。
- 初始管理员由后端按环境变量创建，而非 `init.sql`。
- `relay_tasks` 增加 `sending` 状态用于多实例抢占；需求文档只列了 pending/success/failed/dead。
- 字段加密使用 AES-256-GCM（文档只写 AES）。
