# Cron 应用/RPC 组合需求（M23 交付，组合根尚未接线）

本文件记录 `internal/ai/cron`、`internal/ai/guard` 为无人值守定时任务提供的接线点。
本文只做说明：app/RPC 组合根（`internal/app`、`internal/app/production`、前端 IPC）
属于后续 wiring 任务，M23 未改动其中任何文件。

## 持久化

- 迁移 `migrations/0005_cron.sql` 随 `internal/store.Open` 自动应用（`cron_job` 表，
  按 `session_id` 索引）。无需额外迁移步骤。
- 组合根在打开应用库后构造 store：

  ```go
  cronStore, err := cron.NewSQLiteStore(ctx, appStore.DB())
  ```

  构造器会校验 `cron_job` 表存在；库句柄必须由 `internal/store` 打开
  （`_txlock=immediate`），Create 的会话上限检查与插入依赖该串行化写入语义。

- `Scheduler` 每进程一个；`Options.OwnerID` 应保持稳定（默认随机生成亦可，重启后即新 owner，
  租约过期由其它 owner 接管）。`Options.OnError` 接异步存储错误。

## 无人值守 guard 决策（HITL 不得被绕过）

- 无人值守 cron 运行必须通过生产 runner 的 `Permission` 钩子拿到
  `guard.Config{Mode: guard.Unattended}`。`AgentExecutor` 已用
  `guard.WithUnattended(ctx)` 标记 Start 上下文，组合根只需识别该标记：

  ```go
  Permission: func(ctx context.Context) (guard.Config, error) {
      if guard.UnattendedFrom(ctx) {
          return guard.Config{Mode: guard.Unattended}, nil
      }
      return permissions.Snapshot(ctx)
  }
  ```

- Unattended 决策矩阵：Safe 放行；NeedsConfirm/Unknowable/Danger/Forbidden 一律
  显式 deny（原因保留在 ruling 中）。记忆批准（"本次会话允许"）对无人值守无效，
  交互式同意不会授权无人值守执行。
- 纵深防御：`AgentExecutor` 对仍到达事件流的 HITL 中断（如 `ask_user` 提问）立即
  `Runner.Cancel` 并如实返回错误，绝不挂起等待人工；守卫在决策期拒绝的
  NeedsConfirm/Forbidden 不会触发中断，而是工具级 Fail，模型可见并被如实记录。

## Executor 接线

  ```go
  executor := &cron.AgentExecutor{
      Runner: agentRunner, // *agent.Runner 满足 cron.AgentRunner
      // ScopeFor: 可选，把 cron 会话绑定到终端/资产 scope
  }
  scheduler, err := cron.NewScheduler(cronStore, executor, cron.Options{})
  go scheduler.Run(appCtx) // 随应用生命周期启停
  ```

- `Trigger.SessionID` 直接作为 `agent.ChatArgs.ConversationID`：注册任务时必须使用
  已存在的会话 ID（runner 会校验会话存在）。建议 RPC 层以"AI 会话"为 cron 会话。
- 运行结果由 runner 写入会话消息历史；调度器把每次执行的成败/错误（有界 2048 字符）
  CAS 落库到 `cron_job.last_error/consecutive_failures/retry_at`，重启后如实对账，
  不重放已认领执行、不删除其它会话任务。

## 待后续 wiring 任务提供的 RPC 面（不在 M23 scope）

- `cron_register` / `cron_list` / `cron_get` / `cron_set_enabled` / `cron_unregister`：
  直接映射 `Scheduler` 同名方法；unregister 是唯一删除路径，必须携带会话与 job 标识。
- 到点通知：如需前端展示"某任务已开始/结束"，订阅 `Options.OnError` 之外还需在
  executor 外层包一层事件流（adapter 当前只回传 error）。
- 前端管理界面与会话切换逻辑：切换会话只调用 `List(sessionID)`，不得触发删除。
