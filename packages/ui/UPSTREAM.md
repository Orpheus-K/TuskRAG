# UI 上游与组件清单

主题语义变量与 Tailwind v4 配置位于 `src/styles.css`，`Button` 基于 [shadcn/ui Button](https://ui.shadcn.com/docs/components/button)（MIT）扩展变体。

## 已封装高复用组件（`packages/ui/src/components/`）
- 基础表单：`button.tsx`、`input.tsx`、`textarea.tsx`、`select.tsx`、`field.tsx`
- 通用展示：`alert.tsx`、`badge.tsx`、`card.tsx`、`data-table.tsx`、`empty-state.tsx`、`section-header.tsx`
- IDE 工作台布局（纯 CSS 变量驱动折叠与拖拽）：`ide-shell.tsx`、`workspace-split.tsx`
