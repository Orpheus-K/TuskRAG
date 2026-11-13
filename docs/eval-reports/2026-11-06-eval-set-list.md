# 可见题集列表验收

2026-11-06 工作树；R11/R13、M1。交付题集列表领域方法、SQL、GET 接口与生成客户端；编辑页面与批量运行尚未实现。

列表从查询时的当前修订返回名称、题数、摘要、修订号及时间，不返回问题与参考答案。按 ID 分页；SQL 先过滤任一期望位置不可见的题集，再取页大小加一条探测项。服务合并全部目标并按既有锁顺序复核，探测项也在授权范围内；空页仍重新检查成员。页内或探测项在等待期间失效即显式失败，不能返回隐藏题集的名称或下一页游标。接口参数与并发刷新行为见 [13](../13-接口契约概要.md)。

## 验证

- `go test ./internal/eval ./internal/httpapi ./internal/kb` 通过；本机未配置 PG 的场景由下述真实环境验证。
- Linux `go test -race -count=1 ./internal/eval ./internal/httpapi` 通过。
- 真实 PG Linux race `kb.test -test.run '^TestEvalSet' -test.v -test.count=1`：8 项及子场景通过。新增列表验证隐藏项不占页位、当前修订元数据、稳定下一页/末页/空页、非法分页、普通角色/跨租户/无成员及归档读取；真实锁等待验证下一页探测项受限时拒绝整页，空页和非空页均拒绝撤权后返回。
- eval/kb/httpapi 的 vet、golangci-lint 通过（0 issues）。sqlc、Go OpenAPI 与客户端生成完成，`pnpm typecheck`、`git diff --check` 通过。

HTTP 测试使用替身确认身份/知识库/游标/页大小传递、参数错误不进入领域、403/404 映射及空数组/null 游标；不以此替代数据库授权证据。

真实环境为旧测试实例独立容器 `tuskrag-eval-set-list-pg-20261106`，镜像 `tuskrag-eval:20261029`，回环 56446，隔离库 `tuskrag_test_eval_set_list`。复用已验证迁移二进制部署到版本 22；本次无新迁移。收尾 tenant、eval_set、eval_set_revision、idempotency_record 均为 0，专用容器和匿名卷已删除。

本次不涉及页面或检索执行，未运行浏览器和模型质量评测。完整 T12/M1 尚未完成。下一步：题集列表、创建、编辑和修订冲突的控制台闭环。
