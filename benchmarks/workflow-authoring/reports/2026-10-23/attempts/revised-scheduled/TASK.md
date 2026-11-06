你在一个全新目录中完成下面的实际工作流搭建任务。只使用这个目录里的 ./agentflow 二进制、skill/SKILL.md、fixtures 和标准 Python/Node/shell 工具。不要读取其他目录、源码仓库、历史任务或 golden/reference 答案，不要联网、调用真实模型、发送真实消息。你可以使用工具的 schema/help/validate/dry-run 等能力发现接口。先阅读 skill/SKILL.md，然后完成任务。

业务需求：
给一个产品研究团队搭建本地工作流。每个工作日 Asia/Shanghai 09:15 自动启动：先由 Codex 起草改进提案，输出 summary/check；再调用 fixtures/channel.py 发给 reviewer 输入指定的人审核；审核返回 signal（happy/again/sad）与 note。happy 后交给 Codex 输出 next_step/verification，again 带上 note 回到起草（最多返工两次），sad 正常结束且不运行最后一步。最后一步必须拿到最新提案和审核意见。topic/reviewer 是必填运行输入。channel.py 的 JSON 输入必须含 recipient、content、actions，actions 是 label/output 对象数组，输出是审核 JSON。这个程序是本地模拟渠道，不需要联网或另起扫描 workflow 的服务。为各个步骤定义输入输出契约，写好 executor 入口配置和 cron 配置，说明如何启动定时器。

交付要求：在这个目录生成 workflow JSON、自定义 executor 配置/程序（如需要）、RUNBOOK.md，以及 submission.json。submission.json 格式为 {"workflow":"相对路径","roles":{逻辑角色:实际节点ID}}，本题逻辑角色是 ["draft", "review", "finalize"]；角色映射只是供测试定位节点，你可以自由选择节点 ID。RUNBOOK.md 说明执行方法、推进方式和已验证/未验证内容。不要调用 Codex/Claude 模型执行交付的流程；模型步骤将由评测器用确定性程序替代。非模型程序可在本地测试。不需要向用户提问，需求已完整给出。只在当前工作目录工作。
