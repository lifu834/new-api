# nycatai fork 变更记录 — 2026-06-03/04

> 本仓为 new-api 的 nycatai 定制 fork。本轮后端改动汇总。完整方案见 `nycatai-ops/docs/ops/`。
> （本文件为 nycatai 运维记录，不涉及 new-api/QuantumNous 上游标识。）

## 已上线 commit `e683efb` — 登录自适应 PoW（反撞库）

**目的**：抬高撞库盗号成本（已充值账号有余额=最高 payoff 攻击），**对正常用户零摩擦**（仅某用户名连续失败≥阈值后才要 PoW）。

**flag 默认关**（`LOGIN_POW_ENABLED=false`）→ 关时 Login 行为与原先逐行一致。生产已部署并 `LOGIN_POW_ENABLED=true` 激活、实测第6次坏登录返回 `pow_required`。

### 改动文件
| 文件 | 改动 |
|---|---|
| `common/constants.go` + `common/init.go` | 新增 env：`LOGIN_POW_ENABLED`(false) / `LOGIN_POW_DIFFICULTY`(15) / `LOGIN_POW_FAIL_THRESHOLD`(5) |
| `service/login_pow.go`（新）| hashcash PoW 签发/校验（内存存储，单节点正确，单次有效+2min TTL）+ 按用户名失败计数(15min TTL) |
| `controller/pow.go`（新）| `GET /api/pow/challenge` 签发挑战（关闭时返回 enabled:false）|
| `controller/user.go` | `Login`：失败≥阈值→要求 PoW（先验PoW再验密码），成功清零、失败+1。全部由 `if common.LoginPowEnabled` 守卫 |
| `router/api-router.go` | 挂 `/api/pow/challenge` 路由 |

### PoW 协议（前端 nycatai `utils/pow.ts` 必须一致）
challenge `{id, seed(hex), difficulty(前导零bit数), expires_at}`；求解 nonce 使 `SHA256(seed+":"+nonce)` 有 difficulty 个前导零 bit；提交 `{challenge_id, nonce}`。

### 开关/回滚
docker-compose 加/删 `LOGIN_POW_ENABLED=true` 重启即开/关。难度调 `LOGIN_POW_DIFFICULTY`。

### 部署
US：`cd /opt/newapi/new-api && git pull origin main` → `cd /opt/newapi && docker compose build newapi && docker compose up -d --no-deps newapi`（构建失败不动运行容器；cookie 会话不登出）。

## ⏳ 待办（未改代码）
- 🟠 **X-Group-Override 治本**（`middleware/auth.go:402-406`）：override 应只改 UsingGroup(路由)、不改 UserGroup(身份/定价)。2026-06-04 已确认该 header 可经默认 `/v1/` 路径被客户端注入薅企业价，**已在 HK Caddy 边缘 `header_up -X-Group-Override` 止血**；后端治本作防御纵深。设计见 `nycatai-ops/docs/ops/group-ratio-override-design.md`。
- 🟢 登录失败/PoW触发**加一行日志**，供 ts-monitor 精确告警（现为登录量洪水告警）。
- 🟢 `controller/misc.go:267` EmailAliasRestriction 现拦 `+` 与 `.` → 若启用需改成只拦 `+`（`.` 是正常邮箱常见字符）。
