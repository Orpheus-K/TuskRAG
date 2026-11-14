---
name: tuskrag-ui
description: TuskRAG 前端 UI/UX 与组件复用规范：要求采用清爽 IDE 式左侧导航 + 纯 CSS 驱动的可折叠可拖拽多栏（左-中-右-右）工作台布局，禁止业务页面直接编写原生 HTML 表单/按钮/表格标签，必须复用 `@tuskrag/ui`。
---

# TuskRAG 界面与组件开发规范

在 `apps/web` 新增、重构或评审界面时，**必须遵循以下约束**（与 [.agents/rules/code.md](../../rules/code.md) 一致）：

## 1. 布局架构：清爽 IDE 式多栏工作台
- **全局壳层（`IdeShell`）**：
  - 登录后统一使用 `@tuskrag/ui/ide-shell`：左侧可折叠导航栏（全局入口 + 当前知识库上下文分组子菜单）+ 顶部状态与面包屑栏 + 全宽自适应主工作区。
  - **禁止**在已登录页面中恢复顶部堆叠操作栏或把所有设置/表格纵向长滚动塞进单栏 `max-w-*` 容器。
- **多栏可折叠/可拖拽工作区（`WorkspaceSplit`）**：
  - 支持 2 栏（左-右）、3 栏（左-中-右）乃至 4 栏（左-中-右-右）工作台。
  - **性能硬约束**：折叠通过 `data-collapsed="true"` + CSS `grid-template-columns` 选择器与 `transition` 实现；拖拽通过分隔条 `pointermove` 直接更新容器 CSS 自定义属性 `--split-template`，**禁止**在拖拽过程中触发 React `setState` 或 JavaScript 几何重算循环，仅在 `pointerup` 时写入 `localStorage`。
  - 主工作栏省略 `defaultWidth` 保持 `minmax(0, 1fr)` 自适应；辅助/检查器面板配置 `defaultWidth`、`minWidth`、`maxWidth` 与 `collapsible: true`。

## 2. 组件封装与零原生控件原则
- **禁止在 `apps/web/src/*.tsx` 业务代码中直接编写原生交互/结构控件**：
  - `<input>` → `@tuskrag/ui/input` (`Input`)
  - `<textarea>` → `@tuskrag/ui/textarea` (`Textarea`)
  - `<select>` → `@tuskrag/ui/select` (`Select`)
  - `<button>` → `@tuskrag/ui/button` (`Button`)
  - `<table>`（Markdown 渲染映射除外）→ `@tuskrag/ui/data-table` (`DataTable` / `DataTableRow` / `DataTableCell`)
- **通用组合必须复用 `@tuskrag/ui`**：
  - 表单域：`@tuskrag/ui/field` (`Field`)
  - 卡片容器：`@tuskrag/ui/card` (`Card`, `CardHeader`, `CardTitle`, `CardDescription`, `CardContent`, `CardFooter`)
  - 标题栏：`@tuskrag/ui/section-header` (`SectionHeader`)
  - 状态标签与提示：`@tuskrag/ui/badge` (`Badge`)、`@tuskrag/ui/alert` (`Alert`)、`@tuskrag/ui/empty-state` (`EmptyState`)
- **共享 UI 边界**：`packages/ui` 不得导入 `@tuskrag/api-client` 或任何业务状态。新增基础组件时须在 `packages/ui/package.json` 导出并更新 [packages/ui/UPSTREAM.md](../../../packages/ui/UPSTREAM.md)。

## 3. 视觉与可访问性基线
- 仅使用 [packages/ui/src/styles.css](../../../packages/ui/src/styles.css) 定义的语义 Token（`bg-background` / `bg-surface` / `bg-surface-subtle` / `border-border` / `text-muted-foreground` / `text-danger`），保持 Vercel/Linear 风格的细边框、低噪点与亮暗主题自适应。
- 所有表单控件必须绑定可见 `label`（或 `aria-label`），状态反馈必须保留 `role="status"` / `role="alert"`。

## 4. 验收检查
- 改动完成后必须通过：`pnpm typecheck`、`pnpm test:web`、`pnpm build:web`，并核对业务目录下原生 `<input|select|textarea|button>` 残留数为 `0`。
