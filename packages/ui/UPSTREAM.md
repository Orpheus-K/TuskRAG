# UI 上游与组件清单

主题语义变量与 Tailwind v4 配置位于 `src/styles.css`，`Button` 基于 [shadcn/ui Button](https://ui.shadcn.com/docs/components/button)（MIT）扩展变体。

## 已封装高复用组件（`packages/ui/src/components/`）
- 基础表单：`button.tsx`、`input.tsx`、`textarea.tsx`、`select.tsx`、`field.tsx`
- 通用展示：`alert.tsx`、`badge.tsx`、`card.tsx`、`data-table.tsx`、`empty-state.tsx`、`section-header.tsx`
- IDE 工作台布局（纯 CSS 变量驱动折叠与拖拽）：`ide-shell.tsx`、`workspace-split.tsx`

图标使用 [lucide-react](https://lucide.dev)（ISC）。选择器、复选框、折叠区、表单对话框（`dialog.tsx`）、下拉菜单（`dropdown-menu.tsx`）与确认对话框按 shadcn/ui 的 Radix 组合方式封装，由本仓库维护 Tailwind 主题、中文按钮和表单接口。上游均采用 MIT 许可。[Select](https://ui.shadcn.com/docs/components/radix/select) · [Checkbox](https://ui.shadcn.com/docs/components/radix/checkbox) · [Collapsible](https://ui.shadcn.com/docs/components/radix/collapsible) · [Dialog](https://ui.shadcn.com/docs/components/radix/dialog) · [Dropdown Menu](https://ui.shadcn.com/docs/components/radix/dropdown-menu) · [Alert Dialog](https://ui.shadcn.com/docs/components/radix/alert-dialog)

章节路径复用 `HeadingPath`，层级分隔符采用 Lucide ChevronRight。
