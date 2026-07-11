# Web 滚动性能与“重复加载”体感修复任务

- 创建时间：2026-07-06
- 状态：已完成
- 范围：`web/src/App.tsx`、`web/src/components/FileViewer.tsx`、`web/src/components/SessionViewer.tsx`

## 背景

用户反馈 MindFS Web 在不刷新页面时，仅拖动滚动条也会出现“很多已加载数据又在加载”的体感。多模型审查结论一致：这不是普通滚动直接触发全量 API 重新拉取，而是滚动路径上的主线程阻塞与大 DOM 重绘导致的错觉；其中最高置信问题是文件查看器滚动时同步 `JSON.stringify` + `localStorage.setItem`。

## 目标

1. 文件查看器滚动时不再每个 scroll tick 同步写入 localStorage。
2. 滚动位置保存保留功能，但改为合并/延迟持久化，并在卸载时 flush。
3. 降低文件查看器 scroll 回调频率，避免无意义父组件回调。
4. 降低会话视图滚动时重复设置 `showJumpToLatest` 状态的开销。
5. 完成前端类型检查和生产构建验证。

## 验收标准

- [x] `FileViewer` scroll 事件通过 `requestAnimationFrame` 合并回调。
- [x] `App` 文件滚动位置保存通过 debounce 写 localStorage，不在每个滚动事件中同步写。
- [x] 文件滚动位置缓存有容量上限，避免访问文件越多 stringify 成本越高。
- [x] `SessionViewer` 只在跳转按钮可见性变化时更新状态。
- [x] `npm run typecheck` 通过。
- [x] `npm run build` 通过。
- [x] `npx playwright test tests/session-viewer.spec.ts --repeat-each=3` 通过。

## 后续优化候选

- 会话列表虚拟滚动：当前 `visibleSessions.map(...)` 仍是全量渲染。
- 会话详情/长工具输出分块或虚拟化。
- 大文件代码视图按块渲染或对超大文件降级高亮。
