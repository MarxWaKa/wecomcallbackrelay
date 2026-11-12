# 企业微信通讯录回调转发服务（wecom-callback-relay）

企业微信「通讯录同步」的回调服务器只能配置一个 URL。本服务作为该 URL 的**唯一接收方**，完成验签、解密后，把事件异步转发给多个下游系统，并提供管理后台维护消费端、企微参数与死信。

支持三种转发模式：

- **JSON 模式**：转发解密后的 JSON，消费端使用 none / bearer / basic / HMAC 认证。
- **企微兼容模式**：把原始明文 XML 重新加密为企微回调格式并附加新的签名参数，存量消费端保留原有验签解密代码即可接入，实现**无感切换**。
- **原样透传模式**（`wecom_proxy`）：不解密、不重新加密，直接把企微原始加密 Body 和 `msg_signature/timestamp/nonce` 原样转发给消费端。消费端必须与「企微参数」使用同一套 Token/EncodingAESKey/CorpID。

> 需求来源：`企业微信通讯录回调转发服务.md`（v3.0）。
> 本实现采用轻量自研管理后台（Gin + GORM + JWT + Casbin + Vue3 + Element Plus），未直接引入完整 gin-vue-admin 脚手架，详见文末「与需求文档的差异」。

## 功能

- 企微回调 GET URL 验证、POST 事件验签解密，1 秒内响应。
- 三种模式异步转发，按消费端独立配置超时、重试次数与认证方式。
- `relay_tasks` 任务持久化，Worker 每 2 秒轮询并发投递。
- 失败重试（默认 1s/2s/4s/8s/16s，可通过 `RETRY_BACKOFF_SECONDS` 配置），耗尽自动转死信。
- 死信列表、详情、单条/批量重推（单次上限 50）、标记丢弃、单条/批量永久删除（单次上限 100）。
- 消费端与企微参数 CRUD，敏感字段 AES-256-GCM 加密存储、接口脱敏、编辑留空不修改。
- 管理员登录、JWT、Casbin 角色授权（admin / viewer）、修改密码、管理员管理。
- 操作审计、`/healthz` 健康检查、结构化 JSON 日志。

## 架构

```
                          ┌─────────────────────────────────────┐
                          │       管理后台（Vue3 + Element+）     │
                          │ 消费端 / 企微参数 / 死信 / 管理员      │
                          └───────────────┬─────────────────────┘
                                          │ /api/*  (JWT + Casbin)
企业微信 ──GET验证/POST加密事件──>  Gin 后端 ──> MySQL 8.4
                                          │
                 /wecom/callback           │
                 1. 验签解密                │
                 2. 立即返回 success        │
                 3. 事件写入 relay_tasks    │
                                          │
                 Worker（每 2s 轮询）        │
                 4. 按 forward_mode 构造请求 │
                    - json: 明文 JSON + 认证 │
                    - wecom_xml: 重新加密 XML │
                    - wecom_proxy: 原样透传 │
                 5. 并发投递 / 重试 / 死信   │
```

关键设计：

- **响应优先**：入站解密后先入库，再立即回复企微；转发完全异步。
- **配置与流量隔离**：`/wecom/callback` 走企微验签，`/api/*` 走 JWT + Casbin。
- **快照机制**：任务创建时快照 `forward_mode`、`raw_xml`、`json_payload`；原样透传模式额外快照 `raw_encrypted_body` 与 `inbound_query`，重试与人工重推使用相同载荷。
- **多实例安全**：Worker 使用 `SELECT ... FOR UPDATE SKIP LOCKED` 抢占任务。

## 目录结构

```
.
├── server/                    # Go 后端
│   ├── main.go                # 启动、优雅关闭
│   ├── internal/
│   │   ├── api/               # HTTP 处理器（回调、消费端、死信、认证…）
│   │   ├── bootstrap/         # 初始管理员
│   │   ├── config/            # 环境变量配置
│   │   ├── database/          # MySQL 连接与 AutoMigrate
│   │   ├── middleware/        # JWT、Casbin、CORS、日志
│   │   ├── model/             # GORM 模型
│   │   ├── relay/             # 出站 Sender、Worker、退避
│   │   ├── router/            # 路由注册
│   │   ├── security/          # AES-GCM、bcrypt、JWT
│   │   └── wecom/             # 企微加解密、XML→JSON 映射
│   └── Dockerfile
├── web/                       # Vue3 管理后台
│   ├── src/{api,layout,router,store,views}/
│   ├── Dockerfile
│   └── nginx.conf             # 静态资源 + /api /wecom 反向代理
├── init.sql                   # 业务表 DDL
├── docker-compose.yml
├── .env.example
└── README.md
```

## 快速部署（Docker Compose）

前置条件：Linux 服务器已安装 Docker 与 Docker Compose v2，80/443（或自定义 `FRONTEND_PORT`）端口可用；MySQL/backend 仅 Docker 内部访问。

```bash
cp .env.example .env
# 修改 .env：MYSQL_PASSWORD、JWT_SECRET、CONFIG_ENCRYPT_KEY、ADMIN_PASSWORD
vim .env

docker compose up -d --build
docker compose ps
docker compose logs -f backend
```

启动后：

1. 访问 `http://服务器IP:8889/`（默认 `FRONTEND_PORT=8889`），使用 `ADMIN_USERNAME / ADMIN_PASSWORD` 登录。生产环境建议通过外层 HTTPS Nginx 访问。

2. 在「企微参数」填写 CorpID、Secret、Token、EncodingAESKey。

3. 在「消费端管理」新增消费端：
   - 存量系统选「企微兼容模式」，填写其原有 Token / EncodingAESKey / CorpID。
   - 如果消费端与「企微参数」使用同一套密钥，可选「原样透传」，无需再填密钥。
   - 新系统选「JSON 模式」，配置认证方式。

4. 在企微管理后台把「通讯录同步」回调 URL 设置为 `https://你的域名/wecom/callback`，保存时自动完成 URL 验证。

   回调地址：

   ```
   https://wecomcallback.example.com/wecom/callback
   ```

   

5. 触发一次通讯录变更（如新增测试成员），在「概览」确认任务已投递。

> 生产环境请在 frontend 前再放一层 HTTPS 反向代理（Nginx / SLB），并仅暴露 443。企微要求回调地址可公网访问且为 HTTPS。

### 外层 Nginx 示例

`deploy/nginx/wecom-relay.conf` 提供了一份外层反向代理配置（参考现网外层 Nginx 风格），默认策略：

- 仅 `/wecom/callback` 对公网开放，供企业微信回调使用。
- 管理后台 `/`、`/api/`、`/healthz` 仅允许内网/VPN 网段访问。
- 全部请求转发到前端容器（默认 `8889`），由容器内 nginx 再转发到 backend。
- 已包含 TLS、健康检查、批量重推所需的 180s 读超时。

使用前替换文件顶部的域名、证书路径与 upstream 地址，然后执行 `nginx -t && nginx -s reload`。

### 端口

| 服务 | 容器端口 | 宿主机端口 | 说明 |
| :--- | :--- | :--- | :--- |
| frontend | 80 | `${FRONTEND_PORT}`=8889 | 管理后台 + `/api`、`/wecom` 反向代理，唯一对宿主机暴露的端口 |
| backend | 8888 | 不映射 | 仅 Docker 内部网络，由 frontend nginx 代理 |
| mysql | 3306 | 不映射 | 仅 Docker 内部网络，由 backend 访问 |

## 配置项

| 环境变量 | 必填 | 默认值 | 说明 |
| :--- | :---: | :--- | :--- |
| `MYSQL_PASSWORD` | 是 | - | MySQL root 密码（compose 注入为 `MYSQL_ROOT_PASSWORD`） |
| `MYSQL_USER` | 否 | `root` | 后端连接 MySQL 的用户（只传给 backend 的 `DB_USER`，非 root 账号需手动创建） |
| `MYSQL_TCP_PORT` | 否 | `3306` | MySQL 容器端口 / 后端 `DB_PORT` |
| `MYSQL_DB_NAME` | 否 | `wecom_relay` | 数据库名 |
| `FRONTEND_PORT` | 否 | `8889` | 前端对外端口 |
| `BACKEND_PORT` | 否 | `8888` | 后端容器内部端口；必须与 `web/nginx.conf` 的 8888 一致 |
| `JWT_SECRET` | 是 | - | 至少 16 字符，建议 32+ 随机串 |
| `JWT_EXPIRE_HOURS` | 否 | `24` | 登录令牌有效期 |
| `CONFIG_ENCRYPT_KEY` | 是 | - | 敏感字段加密密钥，至少 16 字符；**丢失不可恢复** |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` | 首次启动 | `admin` / - | 初始管理员；已有账号时不会覆盖 |
| `WORKER_INTERVAL_SECONDS` | 否 | `2` | 任务轮询间隔 |
| `RETRY_BACKOFF_SECONDS` | 否 | `1,2,4,8,16` | 重试退避秒数序列，逗号分隔；固定 30 分钟填 `1800` |
| `WORKER_CONCURRENCY` | 否 | `8` | 单批并发投递数 |
| `CALLBACK_MAX_SKEW_SECONDS` | 否 | `300` | 入站时间戳允许偏差 |
| `CORS_ALLOW_ORIGINS` | 否 | `*` | 管理 API 跨域白名单，逗号分隔 |
| `LOG_LEVEL` | 否 | `info` | debug / info / warn / error |
| `RELAY_LOG_RAW_EVENTS` | 否 | `false` | DEBUG 日志是否输出明文事件/JSON Body（含 PII），仅临时排查开启 |

> `init.sql` 由 MySQL 容器首次初始化时自动在 `MYSQL_DATABASE`（即 `MYSQL_DB_NAME`）中执行；即使修改了 `MYSQL_DB_NAME`，后端启动时的 AutoMigrate 也会自动补齐全部业务表。
> compose 默认使用 `MYSQL_USER=root` 作为后端连接用户（不会作为 MySQL 容器的 `MYSQL_USER`，因为官方镜像不允许 `MYSQL_USER=root`）。如需非 root 账号，请先在 MySQL 中手动创建并授权，再把 `MYSQL_USER` / `MYSQL_PASSWORD` 改为该账号。
> 直接运行后端（非 compose）时使用 `DB_HOST` / `DB_PORT` / `DB_USER` / `DB_PASSWORD` / `DB_NAME` 和 `SERVER_PORT`；compose 会自动从上面的 `MYSQL_*` / `BACKEND_PORT` 映射。

## 本地开发

### 后端

依赖 Go 1.27+、MySQL 8.4+。

```bash
cd server
# 设置环境变量（可参考 .env.example，DB_HOST 指向本地 MySQL）
export DB_HOST=127.0.0.1 DB_PORT=3306 DB_USER=root DB_PASSWORD=xxx DB_NAME=wecom_relay
export JWT_SECRET=local-dev-jwt-secret-1234567890
export CONFIG_ENCRYPT_KEY=local-dev-encrypt-key-1234567890
export ADMIN_USERNAME=admin ADMIN_PASSWORD=Admin@123456
export GIN_MODE=debug
go run .
```

后端启动时自动 AutoMigrate 并创建初始管理员。

### 前端

依赖 Node.js 20+（本项目使用 Node 22）。

```bash
cd web
npm install
npm run dev        # http://127.0.0.1:5173，已配置 /api 与 /wecom 代理到 8888
npm run build      # 产物在 web/dist
```

## 测试

```bash
cd server
go test ./...                                   # 全部测试
go test ./internal/wecom -v                     # 企微加解密/XML 映射
go test ./internal/relay -v                     # Sender、HMAC、三种模式、退避
go test ./internal/api -run TestCallback -v     # 回调集成测试（SQLite）
go vet ./...
```

测试覆盖：

- 官方 Go 加解密库（`github.com/sbzhu/weworkapi_golang/wxbizmsgcrypt`）的 `VerifyURL` / `DecryptMsg` / `EncryptMsg`。
- 企业微信官方固定测试向量（路径 91144）：签名校验与解密结果逐字段比对通过。
- XML → JSON 映射（成员 / 部门 / 标签 / 异步任务事件）、稳定 event_id。
- JSON 模式 none/bearer/HMAC 认证、企微兼容模式端到端验签解密、原样透传 Body/Query 一致性、非 2xx 错误。
- 回调 GET 验证、POST 入库、重复 MsgId 幂等、坏签名/过期时间戳 403、明文解析失败返回 200。
- AES-GCM 敏感配置加解密与脱敏、退避计算。

## API

管理接口统一返回 `{"code":0,"msg":"ok","data":...}`；错误返回对应 HTTP 状态与 `{"code":<status>,"msg":"..."}`。

| 方法 | 路径 | 鉴权 | 说明 |
| :--- | :--- | :--- | :--- |
| GET | `/wecom/callback` | 企微验签 | URL 验证 |
| POST | `/wecom/callback` | 企微验签 | 事件接收 |
| GET | `/healthz` | 无 | 健康检查（含 DB Ping） |
| POST | `/api/base/login` | 无 | 登录 |
| GET | `/api/base/user-info` | JWT | 当前用户 |
| POST | `/api/base/logout` | JWT | 登出 |
| POST | `/api/base/change-password` | JWT | 修改密码 |
| GET | `/api/dashboard/summary` | JWT | 概览统计 |
| GET/POST | `/api/consumers` | JWT+Casbin | 消费端列表 / 新增 |
| GET/PUT/DELETE | `/api/consumers/:id` | JWT+Casbin | 详情 / 修改 / 删除 |
| POST | `/api/consumers/:id/test` | JWT+Casbin | 连通性测试 |
| GET/POST | `/api/wecom-config` | JWT+Casbin | 企微参数列表 / 新增 |
| PUT/DELETE | `/api/wecom-config/:id` | JWT+Casbin | 修改 / 删除 |
| GET | `/api/dead-letters` | JWT+Casbin | 死信列表 |
| GET | `/api/dead-letters/:id` | JWT+Casbin | 死信详情 |
| POST | `/api/dead-letters/:id/replay` | JWT+Casbin | 单条重推 |
| POST | `/api/dead-letters/replay-batch` | JWT+Casbin | 批量重推（≤50） |
| PUT | `/api/dead-letters/:id/discard` | JWT+Casbin | 标记丢弃 |
| DELETE | `/api/dead-letters/:id` | JWT+Casbin | 永久删除单条死信 |
| POST | `/api/dead-letters/batch-delete` | JWT+Casbin | 批量永久删除（≤100） |
| GET/POST | `/api/admin-users` | JWT+admin | 管理员列表 / 新增 |
| PUT/DELETE | `/api/admin-users/:id` | JWT+admin | 修改 / 删除 |
| PUT | `/api/admin-users/:id/reset-password` | JWT+admin | 重置密码 |
| GET | `/api/audit-logs` | JWT | 操作审计 |

角色策略：

- `admin`：可读写全部管理接口。
- `viewer`：仅可 GET；无法修改配置、重推死信或管理管理员。

## 转发协议

### JSON 模式

请求体示例：

```json
{
  "event_id": "evt_msg-1001",
  "source": "wecom_contact_sync",
  "event_type": "change_contact",
  "change_type": "create_user",
  "corp_id": "ww123456",
  "create_time": 1699999999,
  "msg_id": "msg-1001",
  "payload": {
    "user_id": "zhangsan",
    "department": [1, 2]
  }
}
```

请求头：`Content-Type: application/json`、`X-Event-Id: <event_id>`、`User-Agent: wecom-callback-relay/1.0`。

认证方式：

| 类型 | 请求头 |
| :--- | :--- |
| none | 无 |
| bearer | `Authorization: Bearer <token>` |
| basic | `Authorization: Basic base64(user:password)` |
| hmac | `X-Timestamp: <unix秒>`、`X-Signature: hex(HMAC-SHA256(secret, timestamp + "." + body))` |

> 调试：可在「消费端管理」点击「测试」，系统会发送一条 `event_type=test` 的探测事件。

### 企微兼容模式

- 请求体为 `EncryptMsg` 生成的加密 XML。
- URL 追加 `msg_signature`、`timestamp`、`nonce`（保留原有 Query）。
- 消费端用共享的 Token / EncodingAESKey 按企微原逻辑验签解密即可，明文与企微原始明文完全一致。
- 兼容模式下不使用 JSON 认证，验签通过即视为可信来源。
- 外层报文的 `<AgentID>`：真实事件会自动透传入站企微报文中的 AgentID；消费端“测试”按钮使用消费端记录里可选的 AgentID 字段（未填则保持空节点）。
- 出站 `nonce` 为纯数字，与企微回调的随机数形式一致。

### 原样透传模式（`wecom_proxy`）

- 中间件仍会解密入站事件用于日志、任务与 JSON 消费者，但对该模式的消费端**不重新加密**。
- 入站时快照企微原始加密 Body 和原始 Query（`msg_signature/timestamp/nonce`），Worker 原样 POST 给消费端。
- 消费端必须与「企微参数」使用**完全相同**的 Token / EncodingAESKey / CorpID；否则无法解密。
- 优点：消费端收到的请求与企微直连完全一致，可排除重新加密带来的差异。
- 注意：原样透传使用企微原始时间戳；如果消费端严格执行时间戳窗口，异步转发延迟可能导致超窗失败。需要新时间戳时请使用企微兼容模式。
- “测试”按钮会使用「企微参数」的密钥生成一条样本请求，验证消费端能否用共享密钥解密。

### 字段范围

> **CorpID 以真实回调的 `ToUserName` 为准**（企业微信 CorpID 可能是 `wx` 或 `ww` 开头，不要凭前缀推断）。
> 中间件入站解密用「企微参数」里的 CorpID；企微兼容模式出站重加密用消费端记录里的 CorpID。
> 两者应分别与企微后台 receiveID、消费端解密库 receiveID 完全一致。

企微现行通讯录回调只携带 ID，不包含姓名、手机号、部门名等业务信息；本服务原样转发，不做补全：

- 成员事件：`UserID`、`Department`（逗号分隔部门 ID）、`NewUserID`（仅变更时）。
- 部门事件：`Id`、`ParentId`。
- 标签事件（`ChangeType=update_tag`）：`TagId`、`AddUserItems`、`DelUserItems`、`AddPartyItems`、`DelPartyItems`（成员/部门列表均为逗号分隔）。
- 异步任务完成（`Event=batch_job_result`）：`BatchJob.JobId`、`JobType`、`ErrCode`、`ErrMsg`。
- 通用：`Event`、`ChangeType`、`ToUserName`、`FromUserName`、`CreateTime`；`MsgId` / `ReqId` 若存在。
- 通讯录变更事件通常**不带 MsgId**；`event_id` 优先使用 `MsgId`，否则用「明文 SHA1 + CreateTime」生成稳定 ID，保证企微重试时幂等。
- 回调时间戳原生为**秒级**；服务同时兼容 13 位毫秒级时间戳，但仅用于时间窗口判断，签名计算始终使用请求中的原始字符串。

JSON `payload` 中对应的键：

| 事件 | payload 字段 |
| :--- | :--- |
| 成员 | `user_id`、`department`（数字数组）、`new_user_id` |
| 部门 | `id`、`parent_id` |
| 标签 | `tag_id`、`add_user_items`（字符串数组）、`del_user_items`、`add_party_items`（数字数组）、`del_party_items` |
| 异步任务 | `job_id`、`job_type`、`err_code`、`err_msg` |

官方字段依据：

- [成员变更通知](https://developer.work.weixin.qq.com/document/path/96302)
- [部门变更通知](https://developer.work.weixin.qq.com/document/path/96303)
- [标签变更通知](https://developer.work.weixin.qq.com/document/path/96304)
- [异步任务完成通知](https://developer.work.weixin.qq.com/document/path/96305)
- [接收消息与事件概述](https://developer.work.weixin.qq.com/document/path/96466)
- [加解密方案](https://developer.work.weixin.qq.com/document/path/101841)

## 无感切换步骤

1. 在「消费端管理」新增记录：`forward_mode=wecom_xml`（或密钥完全一致时用 `wecom_proxy`），回调地址填存量系统原地址；兼容模式填写存量系统当前 Token / EncodingAESKey / CorpID，透传模式无需再填。
2. 在「企微参数」填写**与存量系统相同**的 Token / EncodingAESKey / CorpID，并填写 Secret。
3. 低峰期在企微后台把通讯录同步回调 URL 改为 `https://你的域名/wecom/callback`，保存时完成验证。
4. 触发一次通讯录变更，确认：
   - 本服务日志：解密成功、任务入库、投递成功。
   - 存量系统：收到加密 XML，原验签解密代码工作正常。
5. 观察稳定后，再接入 JSON 模式的新消费端。

## 死信与运维

### 抓取企微入站/出站报文（排查专用）

1. 在 `.env` 中设置：
   ```dotenv
   LOG_LEVEL=debug
   ```
2. 重启后端：
   ```bash
   docker compose up -d backend
   ```
3. 触发一次企微回调或消费端“测试”，查看日志：
   ```bash
   docker compose logs -f backend | grep -E "企微回调入站请求|企微回调解密明文|企微兼容模式投递载荷|企微兼容模式出站请求|企微原样透传载荷|企微原样透传出站请求|JSON 模式出站请求"
   ```
4. 重点对比：
   - 入站：`query`、`body`（密文）、`to_user_name`、`agent_id`、`encrypt_len`
   - 解密明文：默认只有 `plain_len`/`plain_sha256`；需要原文时设置 `RELAY_LOG_RAW_EVENTS=true`
   - 出站：`url`、`timestamp`、`nonce`、`msg_signature`、`body`（密文）
5. 排查完成后将 `LOG_LEVEL` 改回 `info`，并确保 `RELAY_LOG_RAW_EVENTS=false`。

> - 默认 DEBUG 日志只输出长度/哈希，不落地明文 PII。
> - `RELAY_LOG_RAW_EVENTS=true` 后才会输出解密后的明文 XML、JSON Body、raw_xml，其中包含 `user_id`/`department` 等 PII；仅限临时排查，排查完必须关闭。
> - 任何情况下都不会输出 Token、EncodingAESKey、Secret。

- **自动重试**：默认按 1/2/4/8/16 秒退避；可通过 `RETRY_BACKOFF_SECONDS` 配置，序列耗尽后使用最后一个值。
  - 固定每 30 分钟重试一次：`RETRY_BACKOFF_SECONDS=1800`。
  - 自定义序列示例：`RETRY_BACKOFF_SECONDS=30,60,300,1800`（30 秒、1 分钟、5 分钟、之后固定 30 分钟）。
  - 累计重试次数超过消费端 `max_retry`（默认 5）后转死信。固定 30 分钟 + `max_retry=5` 时，约 2.5 小时后转死信。
  - 原样透传模式使用企微原始时间戳；如果消费端严格校验时间戳窗口，长时间后再重试可能超窗，建议此类消费端使用 `wecom_xml` 或关闭时间戳校验。
- **人工重推**：仅 `pending` / `discarded` 可重推；`replaying` 返回 409 防连点；失败自动回退 `pending` 并记录错误；人工重推不计入自动重试次数。
- **永久删除**：支持单条删除与批量删除（单次 ≤100）；`replaying` 状态的死信会返回 409 / 批量时自动跳过；删除不可恢复，操作会写入审计日志。
- **告警**：pending 死信超过 10 条或最老记录超过 1 小时，后端输出 ERROR 日志（每 10 分钟检查）。
- **清理**：`resolved` 死信保留 30 天、`discarded` 保留 7 天；`success`/`failed` 任务保留 30 天，每日自动清理。
- **日志**：JSON 结构化输出到 stdout，由 Docker json-file 驱动落盘；日志不包含 Secret、Token、AESKey 与请求体明文。
- **备份**：定期备份 MySQL 数据卷；`CONFIG_ENCRYPT_KEY` 必须与数据库备份一起妥善保管，否则敏感字段无法解密。
- **多实例**：Worker 使用 `SKIP LOCKED` 抢占，可水平扩展；异常中断的 `sending` 任务在启动 5 分钟后自动回收。

## 安全说明

- 敏感字段（`secret`、`token`、`encoding_aes_key`、`auth_config`、消费端兼容密钥）使用 AES-256-GCM 加密存储，密钥来自 `CONFIG_ENCRYPT_KEY`（SHA-256 派生）。
- 管理接口全部 JWT + Casbin；登录接口除外。
- 入站时间戳窗口默认 300 秒；验签/解密失败返回 403 且不入库。
- 企微兼容模式与原样透传模式都意味着中间件与消费端共享 Token/AESKey；如需密钥隔离，请使用 JSON 模式 + HMAC。
- 生产环境请使用 HTTPS、强密码、限制 MySQL 端口暴露，并定期轮换管理员密码。

## 与需求文档的差异

| 项目 | 需求文档 | 本实现 |
| :--- | :--- | :--- |
| 管理后台基座 | gin-vue-admin 脚手架 | 轻量自研（Gin + GORM + JWT + Casbin + Vue3 + Element Plus），功能覆盖登录、角色、CRUD、死信 |
| 角色/菜单权限 | Casbin + sys_* 系统表 | 固定 admin / viewer 两种角色 + Casbin 接口级授权；无动态菜单管理 |
| 兼容模式外层报文 | 未明确 | 透传入站 `<AgentID>`，出站 nonce 使用纯数字 |
| 系统表 | gin-vue-admin 初始化 SQL | 仅业务表，`init.sql` + AutoMigrate |
| 初始管理员 | 初始化 SQL | 首次启动按环境变量创建 |
| 字段加密算法 | AES | AES-256-GCM（密钥经 SHA-256 派生） |
| 企微官方库 | 使用官方加解密库 | 使用企业微信官方 Golang 示例库 `github.com/sbzhu/weworkapi_golang/wxbizmsgcrypt`（XML 版），`VerifyURL`/`DecryptMsg`/`EncryptMsg` 均委托官方库；另有官方固定测试向量 |
| 转发模式 | json / wecom_xml | 增加 `wecom_proxy` 原样透传（消费端与「企微参数」共享同一套密钥时使用） |
| 死信删除 | 未定义 | 增加单条/批量永久删除（批量 ≤100，`replaying` 跳过） |
| 任务状态 | pending/success/failed/dead | 增加 `sending`（Worker 抢占中）以保证多实例不重复投递；透传模式额外快照原始加密 Body/Query |

## 常见问题

**企微后台保存回调 URL 失败？**

- 确认 `https://域名/wecom/callback` 公网可访问，且 WAF/CDN 未拦截 GET 参数。
- 检查「企微参数」中的 Token / EncodingAESKey / CorpID 是否与企微后台完全一致。
- 查看后端日志 `URL 验证失败` 的具体原因；时间戳偏差过大会返回 403。

**消费端收不到事件？**

- 确认消费端为启用状态，且新的通讯录变更事件已产生。
- 在「消费端管理」点击「测试」检查连通性与认证配置。
- 查看「死信管理」是否有失败记录与具体错误。

**切换后存量系统验签失败？**

- 企微兼容模式要求消费端的 Token / EncodingAESKey 与中间件转发时使用的完全一致；请检查消费端配置与 `cert` 是否匹配。
- 确认消费端 URL 未额外校验企微回调中的其他参数。

**消费端报 `-40007 BadPaddingException: Given final block not properly padded`？**

- 企业微信使用 PKCS#7 填充到 **32 字节块**，不是 Java 默认的 16 字节 PKCS#5/PKCS#7。
- 消费端必须使用 `AES/CBC/NoPadding`，解密后手动按 32 字节去填充，或直接使用官方 `WXBizMsgCrypt`。
- 使用 `AES/CBC/PKCS5Padding` 时，只要填充长度为 17~32 就会报此错误；直连企微也可能间歇性失败，不能因为直连偶尔正常就认为实现正确。

**忘记管理员密码？**

- 使用另一个 admin 账号在「管理员」页面重置。
- 若唯一管理员密码丢失，可在数据库中删除对应记录后重启服务（需谨慎），或直接更新 `admin_users.password_hash` 为已知 bcrypt 哈希。
