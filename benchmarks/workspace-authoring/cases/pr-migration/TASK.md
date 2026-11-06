# 每日 PR 审查流程迁移

现有 legacy/pr_daily.py 只会搬运 PR 数据并给所有 PR 写固定结论。请用当前
AgentFlow 重做这个流程，让审查角色能按 policy.md 审查传入的非空证据，并让
维护者参与后续决定。请先读 skill/SKILL.md，使用本目录 ./agentflow。

每次从 run input `case` 得到一份 JSON 证据文件路径，包含字符串 pr_id、head、
patch 和 feedback。审查应输出同样四个字段，以及 findings（字符串数组）。
不能用固定空数组/机械脚本冒充代码审查。首选 Claude，只有启动失败时再用
Codex 完成同一个审查职责。每次 run 内的返工应保留各候选自己的上下文。

审查后等待维护者，向人提供原任务身份、head、发现和证据。人的输出为
`{"action":"accept|revise|stop","comments":"字符串"}`：accept 将最后一份
审查结果和本次 action/comments 保存到 output/report.json；revise 回到审查，
将 comments 作为 feedback 并保留同一 pr_id/head/patch，最多返工两次；stop
结束且不生成交付报告。这里先使用本地 CLI 人工回传，不需要发送外部消息。

说明每个职责的上下文/工具选择、数据契约、流转和人工反馈设计，交付简短的
启动/恢复说明。submission.json 指明 workflow 路径和三个节点标识：
`{"workflow":"你的图路径","roles":{"review":"节点ID","human":"节点ID","archive":"节点ID"}}`。

所有写入限定当前目录。不要访问产品源码、历史答案、其他任务或联网，也不要
修改全局 provider 配置。搭建阶段不要调用模型或启动常驻服务；可以做静态检查
和用明确标注的替身检验契约。评测器会独立验收实际图与程序，然后另做真实
角色执行测试。不要向用户提问，给定业务规则已完整。
