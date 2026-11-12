# 企业微信通讯录回调转发服务 需求文档

| 项目     | 内容                                                         |
| :------- | :----------------------------------------------------------- |
| 文档版本 | v3.0（新增消费端无感切换模式）                               |
| 编写日期 | 2026-11-11                                                   |
| 服务名称 | wecom-callback-relay                                         |
| 技术栈   | 后端：Gin + GORM（基于 gin-vue-admin 脚手架）；前端：Vue3（gin-vue-admin 自带）；数据库：MySQL 8.4；部署：Docker Compose |
| 文档范围 | 回调转发核心（含双转发模式）+ 管理后台 + 数据模型 + 部署运维 |

------

## 1. 背景与目标

### 1.1 背景

企业微信“通讯录同步”的回调服务器**只能配置一个 URL**。企业内存在多个需要感知通讯录变更的业务系统（如 Moka、旧 HR 系统等）。为在不覆盖原有回调地址的前提下让多个系统同时接收事件，需要搭建一个中间件作为**唯一回调接收方**，完成验签解密后**转发**给多个下游系统，并提供管理后台维护配置。

同时，存量消费端（旧系统）当前直接对接企微回调、持有自己的 `Token`/`EncodingAESKey` 并执行解密。切换时需尽量**不改造消费端代码**，实现无感切换。

### 1.2 目标

1. 作为企微通讯录同步回调的**唯一入口**，正确处理 URL 验证（GET）与事件接收（POST）。
2. 完成企微要求的**签名校验与消息解密**，**1 秒内**响应企微。
3. 将事件**异步转发**给多个已注册的消费端，支持两种转发模式（见 §4.5）：
   - **JSON 模式**：转发解密后的 JSON，消费端用自定义认证（推荐新接入系统）。
   - **企微兼容模式（无感切换）**：将明文重新加密为企微回调 XML，消费端保留原有解密逻辑，**代码零改动**。
4. 提供**管理后台**：消费端 CRUD、企微参数 CRUD、管理员账号与权限管理、死信查看与人工重推。
5. 使用 **MySQL** 存储，**Docker Compose** 一键部署。

### 1.3 非目标（本期不做）

- 不做多企业（多 corp）支持（预留 `corp_id` 字段，本期单企业）。
- 不做通讯录详情补全查询（本期只转发回调实际携带的字段，见 §5.3）。
- 不做独立消息队列中间件（重试用任务表 + 轮询实现，见 §4.6）。
- 不做代开发应用、第三方应用回调。

------

## 2. 术语与范围

| 术语         | 含义                                                 |
| :----------- | :--------------------------------------------------- |
| 入站请求     | 企业微信 → 本服务（GET 验证 / POST 事件）            |
| 出站请求     | 本服务 → 消费端（转发）                              |
| 消费端       | 注册在案、需要接收事件的下游业务系统                 |
| 企微参数     | Secret、Token、EncodingAESKey（通讯录同步配置）      |
| JSON 模式    | 转发解密后 JSON，消费端用自定义认证                  |
| 企微兼容模式 | 重新加密为企微 XML，消费端保留原解密逻辑（无感切换） |
| 死信         | 自动重试耗尽仍失败、需人工介入的转发任务             |

------

## 3. 总体架构

text

```
                          ┌─────────────────────────────────────┐
                          │          管理后台（Vue3）            │
                          │  消费端CRUD / 企微参数 / 用户权限     │
                          │  死信列表 / 人工重推                  │
                          └───────────────┬─────────────────────┘
                                          │ /api/*  (JWT 鉴权)
企业微信 ──(GET验证/POST加密事件)──>  Gin 后端
                                          │
                                          ├─ /wecom/callback（企微协议，无JWT）
                                          │    1. 验签解密
                                          │    2. 立即返回 success
                                          │    3. 事件写入 relay_tasks 表
                                          │
                                          ├─ 后台 Worker（轮询 relay_tasks）
                                          │    4. 按消费端 forward_mode 构造出站请求：
                                          │       - JSON 模式：明文 JSON + 自定义认证
                                          │       - 企微兼容模式：重新加密 XML + 企微签名参数
                                          │    5. 并发投递；成功标记；失败重试；耗尽转死信
                                          │
                                          └─ MySQL（配置表 / 任务表 / 死信表 / 系统表）
```



关键设计原则：

- **响应优先**：入站先回复企微，转发异步化，不阻塞响应。
- **配置与流量分离**：管理 API（`/api/*`，JWT）与企微回调（`/wecom/callback`，企微验签）路由隔离。
- **双模式转发**：消费端可按需选择 JSON 模式或企微兼容模式，实现存量系统无感切换。
- **可运维**：死信可查、可人工重推；全链路结构化日志。
- **脚手架复用**：登录、权限、CRUD 页面基于 gin-vue-admin。

------

## 4. 功能需求

### 4.1 入站：URL 验证（GET `/wecom/callback`）

**触发**：企微后台保存回调配置时发起。

**请求参数**（Query）：`msg_signature`、`timestamp`、`nonce`、`echostr`（需 URL Decode）。

**处理逻辑**：

1. 校验必填参数，缺失返回 400。
2. 校验 `timestamp` 与当前时间偏差 ≤ 300 秒，超限返回 403。
3. 调用企微官方加解密库 `VerifyURL`，用 `Token` + `EncodingAESKey` 验签并解密 `echostr`。
4. 失败返回 403；成功将解密明文**原样**返回（`Content-Type: text/plain`，无引号无换行）。

**验收**：企微后台点击“保存”通过验证。

### 4.2 入站：事件接收（POST `/wecom/callback`）

**请求参数**（Query）：`msg_signature`、`timestamp`、`nonce`
**请求体**：加密 XML

**处理逻辑**：

1. 参数校验、时间戳窗口校验（同 4.1）。
2. 调用官方库 `DecryptMsg` 验签并解密，得到明文 XML。
3. 解析明文，提取事件类型与字段（见 §5.3），同时**保留原始明文 XML 字符串**（供企微兼容模式重新加密使用）。
4. **立即**返回 `200`，响应体 `success`。
5. 将事件写入 `relay_tasks` 表（状态 `pending`），由后台 Worker 异步处理（见 §4.6）。

**异常处理**：

- 验签/解密失败：返回 403，记 ERROR 日志告警，不转发。
- 明文解析失败：返回 200（避免企微重试风暴），记 ERROR 日志。

**验收**：真实变更时 1 秒内返回 200，日志出现正确明文，`relay_tasks` 新增记录。

### 4.3 可转发字段（重要约束）

企微现行规则（2022-08-15 后新配置的回调）**回调只携带 ID**，不含姓名、手机号、部门名等业务信息。本服务原样转发：

| 事件               | 字段                                                         |
| :----------------- | :----------------------------------------------------------- |
| 成员新增/更新/删除 | `UserID`、`Department`（部门ID列表）、`NewUserID`（仅变更时） |
| 部门新增/更新/删除 | `Id`、`ParentId`                                             |
| 通用               | 事件类型、`ChangeType`、`Event`、`ToUserName`、`CreateTime`、`MsgId`/`ReqId`（若有） |

> 更新成员事件仅在**部门变更或 UserID 变更**时触发；更新部门事件仅在 **ParentId 变更**时触发。下游若需完整信息，凭 ID 自行补全，本服务不做补全。

**JSON 模式载荷**：

json

```
{
  "event_id": "evt_20261111_xxx",
  "source": "wecom_contact_sync",
  "event_type": "change_contact",
  "change_type": "create_user",
  "corp_id": "wwxxxx",
  "create_time": 1699999999,
  "msg_id": "xxxx",
  "payload": {
    "user_id": "zhangsan",
    "department": [1, 2]
  }
}
```



实现时需建立企微明文 XML 标签 → JSON 字段的映射表（如 `<UserID>` → `user_id`）。

### 4.4 转发模式定义

每个消费端通过 `forward_mode` 字段选择转发方式：

| 模式         | 值          | 出站内容                              | 消费端需要什么                           | 适用场景               |
| :----------- | :---------- | :------------------------------------ | :--------------------------------------- | :--------------------- |
| JSON 模式    | `json`      | 解密后 JSON（§4.3）                   | 自定义认证（none/bearer/basic/hmac）     | 新接入系统、可改造系统 |
| 企微兼容模式 | `wecom_xml` | **重新加密的企微 XML** + 企微签名参数 | 原有 Token + EncodingAESKey + 原解密代码 | 存量系统无感切换       |

**企微兼容模式的关键要求**：

- 消费端的 `Token` 与 `EncodingAESKey` 必须与中间件在企微后台配置的**完全一致**（即复用同一套密钥）。
- 中间件转发时，用官方库 `EncryptMsg` 将原始明文 XML 重新加密，并在转发 URL 上附带**新生成**的 `msg_signature`、`timestamp`、`nonce`。
- 消费端收到请求后，沿用原有验签解密逻辑即可，**代码零改动**。

### 4.5 出站：异步转发与重试

**任务发现**：后台 Worker 定时（建议每 2 秒）轮询 `relay_tasks` 中 `status='pending'` 且 `next_retry_at <= NOW()` 的任务。

**转发逻辑**：

1. 读取该事件所有 `enabled=1` 的消费端。
2. 对每个消费端按 `forward_mode` 构造请求：
   - `json`：请求体为 §4.3 JSON，按 §6 附加自定义认证。
   - `wecom_xml`：用快照的明文 XML + 消费端共享的 Token/AESKey 调用 `EncryptMsg` 生成加密 XML；生成新的 `msg_signature`/`timestamp`/`nonce` 拼到 URL Query；请求体为加密 XML。
3. 并发发起 HTTP POST。超时：连接 3s、读 5s（消费端可配 `timeout_ms`）。
4. 成功（HTTP 2xx）：投递记录标记 `success`。
5. 失败：`retry_count + 1`，按指数退避设置 `next_retry_at`（1s/2s/4s/8s/16s）；超过 `max_retry`（默认 5）转入 `dead_letters`。
6. 幂等：JSON 模式请求头带 `X-Event-Id`；企微兼容模式消费端可基于 `MsgId`/`CreateTime` 去重，中间件保证同一事件重试使用相同明文。

**任务表模型**（一条事件 × 一个消费端 = 一条投递记录）：

sql

```
CREATE TABLE relay_tasks (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    event_id VARCHAR(100) NOT NULL,
    consumer_id BIGINT NOT NULL,
    forward_mode VARCHAR(20) NOT NULL,      -- json | wecom_xml（创建时快照）
    raw_xml MEDIUMTEXT,                      -- 原始明文 XML（wecom_xml 模式重加密用）
    json_payload JSON,                       -- JSON 模式载荷
    status VARCHAR(20) NOT NULL DEFAULT 'pending',  -- pending/success/failed/dead
    retry_count INT DEFAULT 0,
    max_retry INT DEFAULT 5,
    next_retry_at DATETIME NULL,
    last_error TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_status_retry (status, next_retry_at),
    INDEX idx_event (event_id)
);
```



**验收**：

- JSON 模式消费端收到含正确 `user_id` 的 JSON。
- 企微兼容模式消费端收到加密 XML，用原 Token/AESKey 能成功验签解密，且明文与企微原始明文一致。

### 4.6 死信管理

**死信记录**（`dead_letters` 表，字段见 §5.2）：保存 `event_id`、`consumer_id`、`forward_mode`、`raw_xml` 与 `json_payload` **快照**、认证/密钥配置**快照**、`last_error`、`status`。

**管理后台死信页面**：

- 列表查询：按消费端、模式、状态、时间范围过滤，分页展示。
- 详情查看：展示原始载荷（JSON 或明文 XML）、最后一次错误、重试次数。
- **单条重推**：`POST /api/dead-letters/:id/replay`
  1. 仅 `pending`/`discarded` 允许重推；`replaying` 返回 409 防连点。
  2. 置 `replaying`，按快照 `forward_mode` 构造请求重发。
  3. 成功 → `resolved`；失败 → 回退 `pending`，更新 `last_error`（人工重推不计入 `retry_count`）。
  4. 写审计日志（操作人、时间、结果）。
- **批量重推**：单次上限 50 条，串行或低并发，返回成功/失败明细。
- **标记不再重推**：`discarded`。

**告警与清理**：

- `pending` 死信数超阈值（如 10）或最老记录超 1 小时未处理 → ERROR 日志告警。
- `resolved` 保留 30 天、`discarded` 保留 7 天后清理，每日定时任务执行。

### 4.7 管理后台

基于 gin-vue-admin 自带能力 + 代码生成器生成业务 CRUD。

| 模块             | 功能                                                         | 来源                    |
| :--------------- | :----------------------------------------------------------- | :---------------------- |
| 登录认证         | 管理员登录、JWT、登出、修改密码                              | gin-vue-admin 自带      |
| 管理员管理       | 用户增删改查、启用/禁用、重置密码                            | gin-vue-admin 自带      |
| 角色与权限       | 角色 CRUD、菜单权限、API 权限（Casbin）                      | gin-vue-admin 自带      |
| 消费端管理       | 增删改查、启用/禁用、选择转发模式、认证配置（脱敏）、企微兼容模式密钥配置（脱敏）、连通性测试（可选） | 代码生成器生成 + 自定义 |
| 企微参数管理     | 单条记录增删改查，Secret/AESKey 脱敏显示，编辑留空表示不修改 | 代码生成器生成          |
| 死信管理         | 列表、详情、单条/批量重推、标记丢弃                          | 自定义页面 + 后端接口   |
| 操作审计（可选） | 记录管理端关键操作                                           | 自定义                  |

**安全要求**：

- `secret`、`encoding_aes_key`、`auth_config`、消费端兼容模式密钥在列表与详情中**脱敏**，编辑时留空表示不修改。
- 敏感字段**加密存储**（AES，密钥来自环境变量），**禁止**写入日志。
- 管理 API 全部走 JWT + Casbin 鉴权；`/wecom/callback` 不走 JWT，走企微验签。

### 4.8 接口清单

| 方法                | 路径                             | 鉴权       | 用途          |
| :------------------ | :------------------------------- | :--------- | :------------ |
| GET                 | `/wecom/callback`                | 企微验签   | URL 验证      |
| POST                | `/wecom/callback`                | 企微验签   | 事件接收      |
| GET                 | `/healthz`                       | 无         | 健康检查      |
| POST                | `/api/base/login`                | 无         | 管理员登录    |
| GET/POST/PUT/DELETE | `/api/consumers`                 | JWT+Casbin | 消费端 CRUD   |
| GET/POST/PUT/DELETE | `/api/wecom-config`              | JWT+Casbin | 企微参数 CRUD |
| GET                 | `/api/dead-letters`              | JWT+Casbin | 死信列表      |
| POST                | `/api/dead-letters/:id/replay`   | JWT+Casbin | 单条重推      |
| POST                | `/api/dead-letters/replay-batch` | JWT+Casbin | 批量重推      |
| PUT                 | `/api/dead-letters/:id/discard`  | JWT+Casbin | 标记丢弃      |

------

## 5. 数据模型（MySQL 4）

### 5.1 业务表

sql

```
CREATE TABLE consumers (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    name VARCHAR(100) NOT NULL,
    callback_url VARCHAR(500) NOT NULL,
    forward_mode VARCHAR(20) NOT NULL DEFAULT 'json',  -- json | wecom_xml
    auth_type VARCHAR(20) NOT NULL DEFAULT 'none',     -- json 模式使用
    auth_config JSON,                                   -- json 模式认证配置
    -- wecom_xml 模式专用（与企微后台配置一致的密钥，加密存储）
    wecom_token VARCHAR(200),
    wecom_encoding_aes_key VARCHAR(500),
    wecom_corp_id VARCHAR(100),
    enabled TINYINT NOT NULL DEFAULT 1,
    timeout_ms INT DEFAULT 5000,
    max_retry INT DEFAULT 5,
    created_by BIGINT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at DATETIME NULL
);

CREATE TABLE wecom_config (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    corp_id VARCHAR(100),
    secret VARCHAR(500) NOT NULL,           -- 加密存储
    token VARCHAR(200) NOT NULL,
    encoding_aes_key VARCHAR(500) NOT NULL, -- 加密存储
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at DATETIME NULL
);
```



### 5.2 任务表与死信表

`relay_tasks` 见 §4.5。

sql

```
CREATE TABLE dead_letters (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    event_id VARCHAR(100) NOT NULL,
    consumer_id BIGINT NOT NULL,
    consumer_name VARCHAR(100),
    forward_mode VARCHAR(20) NOT NULL,
    raw_xml MEDIUMTEXT,
    json_payload JSON,
    auth_type VARCHAR(20),
    auth_config JSON,                       -- 快照
    wecom_token_snapshot VARCHAR(200),      -- 快照（脱敏展示）
    wecom_aes_key_snapshot VARCHAR(500),    -- 快照（加密存储）
    last_error TEXT,
    retry_count INT DEFAULT 0,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',  -- pending/replaying/resolved/discarded
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    resolved_at DATETIME NULL,
    INDEX idx_status (status),
    INDEX idx_consumer (consumer_id)
);
```



### 5.3 字段映射与系统表

- 企微 XML → JSON 映射表在实现文档中补全。
- 系统表（`sys_users`、`sys_roles`、`sys_menus`、`sys_apis`、`casbin_rule` 等）由 gin-vue-admin 初始化 SQL 创建。

------

## 6. 出站认证方式

### 6.1 JSON 模式认证

| 类型         | 实现                                                         |
| :----------- | :----------------------------------------------------------- |
| none         | 不加认证头                                                   |
| bearer       | `Authorization: Bearer <token>`                              |
| basic        | `Authorization: Basic base64(user:pass)`                     |
| hmac（推荐） | 对 `timestamp + "." + body` 用 secret 做 HMAC-SHA256，放 `X-Signature`；同时带 `X-Timestamp` |

### 6.2 企微兼容模式认证

不使用自定义认证，而是**复用企微协议本身**：

- 请求体为 `EncryptMsg` 生成的加密 XML。
- URL Query 带 `msg_signature`、`timestamp`、`nonce`。
- 消费端用共享的 Token/AESKey 验签解密，验签通过即视为来源可信。

> 安全边界：企微兼容模式下，消费端与中间件共享同一套 Token/AESKey。若需密钥隔离，应改用 JSON 模式 + HMAC。

------

## 7. 非功能需求

| 类别   | 要求                                                         |
| :----- | :----------------------------------------------------------- |
| 性能   | 入站响应 P99 < 500ms（不含转发）；单机 ≥ 50 QPS；Worker 轮询间隔 2s；重加密开销可接受 |
| 可靠性 | 转发失败自动重试；任务表持久化，重启不丢任务；死信可人工重推 |
| 安全   | 验签失败拒绝；时间戳窗口校验；敏感字段加密存储、页面脱敏、日志脱敏；管理 API JWT+Casbin；JSON 模式支持 HMAC |
| 可观测 | 结构化日志（入站摘要、解密结果、模式、投递目标/状态码/耗时、重试次数、死信告警）；`/healthz`；可选 Prometheus |
| 部署   | Docker Compose 编排 MySQL + 后端 + 前端；配置通过环境变量或挂载配置文件注入 |
| 兼容   | 使用企微官方加解密库，不手写 AES；兼容模式下 `EncryptMsg`/签名生成须与企微规则一致 |

------

## 8. 部署方案（Docker Compose）

yaml

```
services:
  mysql:
    image: mysql:8.4
    environment:
      MYSQL_ROOT_PASSWORD: ${MYSQL_ROOT_PASSWORD}
      MYSQL_DATABASE: wecom_relay
    volumes:
      - mysql_data:/var/lib/mysql
      - ./init.sql:/docker-entrypoint-initdb.d/init.sql
    ports:
      - "3306:3306"
    logging:
      driver: "json-file"
      options:
        max-size: "20m"
        max-file: "3"

  backend:
    build: ./server
    depends_on:
      - mysql
    environment:
      DB_HOST: mysql
      DB_PORT: 3306
      DB_USER: root
      DB_PASSWORD: ${MYSQL_ROOT_PASSWORD}
      DB_NAME: wecom_relay
      CONFIG_ENCRYPT_KEY: ${CONFIG_ENCRYPT_KEY}
    ports:
      - "8888:8888"
    logging:
      driver: "json-file"
      options:
        max-size: "50m"
        max-file: "5"
        compress: "true"

  frontend:
    build: ./web
    depends_on:
      - backend
    ports:
      - "80:80"
    logging:
      driver: "json-file"
      options:
        max-size: "20m"
        max-file: "3"

volumes:
  mysql_data:
```



**部署步骤**：

1. 准备 `.env`（数据库密码、加密密钥）。
2. `docker compose up -d --build`。
3. 登录管理后台，在“企微参数管理”填写 Secret/Token/EncodingAESKey。
4. 在“消费端管理”添加消费端，选择转发模式：
   - 存量系统选 `wecom_xml`，填入与企微后台一致的 Token/AESKey。
   - 新系统选 `json`，配置认证方式。
5. 将企微后台通讯录同步回调 URL 改为 `https://域名/wecom/callback` 并保存验证。
6. 触发一次通讯录变更，验证各消费端接收正常。

------

## 9. 日志与告警

- **INFO**：入站摘要、验签结果、投递结果（consumer/mode/status/耗时）、管理端关键操作。
- **WARN**：重试中、时间戳超窗、消费端连续失败。
- **ERROR**：验签/解密失败、重试耗尽（死信）、配置缺失、死信积压超阈值、兼容模式重加密失败。
- **禁止**：Secret、EncodingAESKey、auth_config、消费端密钥明文入日志。

------

## 10. 测试要求

1. **单元测试**：验签成功/失败、解密成功/失败、时间戳窗口、XML→JSON 映射、HMAC 签名、退避计算、**EncryptMsg 重加密与签名生成正确性**。
2. **集成测试**：
   - 模拟 GET 验证返回明文。
   - 模拟 POST 加密事件，断言 200、任务入库、Worker 投递成功。
   - **企微兼容模式**：断言消费端收到的加密 XML 能用共享 Token/AESKey 验签解密，且明文与原始一致。
   - **JSON 模式**：断言消费端收到正确 JSON 且 HMAC 可验证。
   - 验签失败返回 403 且不入库。
3. **异常测试**：消费端超时/500 触发重试；耗尽转死信；重推防重复（409）；批量上限；管理 API 未授权 401/403；兼容模式密钥错误时的失败与死信。
4. **性能测试**：50 QPS 入站 P99 < 500ms；重加密模式下 Worker 吞吐可接受。

------

## 11. 交付物

1. 后端源码（含 relay 模块、双模式转发、gin-vue-admin 集成）+ 前端构建产物。
2. `init.sql`（业务表 + 系统表 + 初始管理员）。
3. `docker-compose.yml`、Dockerfile、`.env.example`。
4. 单元与集成测试（含兼容模式验签解密用例）。
5. 部署与运维手册（含**无感切换操作步骤**）。
6. 接口文档（管理 API、JSON 载荷、企微兼容模式签名规则）。

------

## 12. 里程碑

| 阶段 | 内容                                                         |
| :--- | :----------------------------------------------------------- |
| M1   | gin-vue-admin 跑通 + Docker Compose 基础编排 + 登录可用      |
| M2   | 入站 GET 验证 + POST 解密 + 任务入库（打通企微后台）         |
| M3   | JSON 模式异步转发 + 自定义认证 + 重试                        |
| M4   | **企微兼容模式**：`EncryptMsg` 重加密 + 签名参数生成 + 消费端零改动验证 |
| M5   | 消费端/企微参数 CRUD 页面 + 敏感字段脱敏加密                 |
| M6   | 死信管理（列表 + 单条/批量重推 + 告警清理）                  |
| M7   | 测试补全、性能验证、部署与切换文档                           |

------

## 13. 无感切换操作说明（专章）

**目标**：存量消费端代码零改动，仅通过中间件转发企微兼容模式 XML，实现平滑切换。

**前置条件**：

- 中间件已部署并通过自测。
- 已获取存量消费端当前使用的 `Token`、`EncodingAESKey`、`CorpID`。

**步骤**：

1. 在管理后台“消费端管理”新增一条记录：
   - `forward_mode = wecom_xml`
   - `callback_url` = 存量消费端原回调地址
   - `wecom_token` / `wecom_encoding_aes_key` / `wecom_corp_id` = 存量系统当前使用的值
2. 在“企微参数管理”中，将 `Token`、`EncodingAESKey` 配置为**与存量系统相同**的值（因为企微后台切换 URL 后，验签解密由中间件承担，但兼容模式转发需复用同一套密钥）。
3. 低峰期，在企微后台将通讯录同步回调 URL 改为 `https://域名/wecom/callback`，保存时完成 URL 验证。
4. 触发一次通讯录变更（如新增测试成员），确认：
   - 中间件日志显示解密成功、任务入库、投递成功。
   - 存量消费端日志显示收到加密 XML、验签解密成功、业务处理正常。
5. 观察一段时间，确认无异常后，再按需接入 JSON 模式的新消费端。

**注意**：

- 切换后，存量消费端不再直接接收企微请求，但其原有解密逻辑仍工作。
- 若存量消费端要求 URL 参数名或 XML 结构与企微完全一致，中间件兼容模式已按企微规则生成，无需额外适配。
- 密钥共享意味着消费端仍持有 Token/AESKey；如需密钥隔离，后续可将其迁移为 JSON 模式 + HMAC。

------

## 14. 风险与待确认项

1. **字段完整性**：本期只转发 ID，需与各消费端确认可接受（见 §4.3）。
2. **Worker 可靠性**：多实例部署需任务抢占（`SELECT ... FOR UPDATE SKIP LOCKED`）或单实例 Worker。
3. **加密密钥管理**：`CONFIG_ENCRYPT_KEY` 保管方式需确认；密钥丢失将无法解密已存敏感字段。
4. **兼容模式密钥共享**：消费端与中间件共享 Token/AESKey，安全边界需评估；建议后续迁移到 JSON+HMAC。
5. **回调 URL 切换**：修改企微后台回调 URL 会清除旧配置，需低峰期操作并提前准备好兼容模式消费端。
6. **gin-vue-admin 版本升级**：relay 模块保持独立包 + 独立路由注册，便于升级脚手架。
7. **重加密性能**：事件量大时 `EncryptMsg` 有 CPU 开销，需在性能测试中验证。