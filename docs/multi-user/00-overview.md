# 多用户支持 — 总览

> 创建日期：2026-05-17

## 现状

Quartet 是纯单用户架构：

| 维度 | 现状 |
|---|---|
| 认证 | 环境变量 `X_AGENT_AUTH` 存储 shared secret，请求头 `X-AGENT-AUTH` 传递 token，不关联任何用户身份 |
| 用户模型 | 无。settings.json 里的 username/avatar 仅用于展示 |
| 数据隔离 | 所有数据共享 `$LOCAL_MEMORY`，无按用户分目录/分库 |
| 前端 | token 存 localStorage，AuthGate 通过 `/api/v1/health` 探测是否需要认证，有 token 时用受保护接口验证有效性，但 token 不关联用户身份 |
| 存储 | 纯文件系统（JSON/JSONL），无数据库 |
| 部署 | 单进程 `127.0.0.1:8090`，本机自用 |

## 多用户目标

1. 支持用户注册、登录（账密 / OAuth）
2. 每个用户拥有独立的 workspace、job、session、settings、usage stats
3. 可选的管理员角色（用户管理、全局配置）
4. 保持向后兼容：单用户场景零配置可用

## 模块拆分

| 编号 | 模块 | 文档 | 说明 |
|---|---|---|---|
| 01 | 用户模型与认证 | [01-auth.md](./01-auth.md) | User 模型、注册/登录、JWT、会话管理 |
| 02 | 存储层改造 | [02-storage.md](./02-storage.md) | 数据目录按用户隔离、路径解析、迁移策略 |
| 03 | API 层改造 | [03-api.md](./03-api.md) | 中间件注入 UserContext、路由鉴权、权限控制 |
| 04 | 前端改造 | [04-frontend.md](./04-frontend.md) | 登录页、用户状态管理、路由与 UI 变更 |
| 05 | 配置与设置 | [05-settings.md](./05-settings.md) | 全局配置 vs 用户配置分离、Model 共享策略 |
| 06 | 共享与协作 | [06-sharing.md](./06-sharing.md) | Job 分享、workspace 协作、权限模型 |
| 07 | 部署与运维 | [07-deployment.md](./07-deployment.md) | 多用户部署模式、数据库选型、容量与安全 |

## 核心设计原则

1. **渐进式**：先落地最小可用（账密登录 + 数据隔离），再迭代（OAuth、角色、协作）
2. **向后兼容**：环境变量 `QUARTET_MULTI_USER=true` 启用；未启用时行为不变
3. **存储可选**：单用户继续用文件；多用户引入 SQLite 做用户表和索引，数据仍可文件存储
4. **Context 透传**：现有 service 层统一通过 Context 传递用户身份，所有路径解析、数据读写都感知当前用户
