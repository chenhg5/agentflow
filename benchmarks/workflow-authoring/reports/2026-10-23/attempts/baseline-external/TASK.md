你在一个全新目录中完成下面的实际工作流搭建任务。只使用这个目录里的 ./agentflow 二进制、skill/SKILL.md、fixtures 和标准 Python/Node/shell 工具。不要读取其他目录、源码仓库、历史任务或 golden/reference 答案，不要联网、调用真实模型、发送真实消息。你可以使用工具的 schema/help/validate/dry-run 等能力发现接口。先阅读 skill/SKILL.md，然后完成任务。

业务需求：
搭一个可持久暂停的文案工作流：Codex 写文案并输出 summary/check；之后等另一个独立终端程序提交任意 JSON，无需启动通知程序。外部输出约定 mood 是 go/back/stop，feedback 是字符串，confidence 是 0 到 1 的数字。只有 mood=go 且 confidence >= 0.8 才进入 Codex 的整理步骤（输出 next_step/verification）；mood=back 或 mood=go 但 confidence<0.8 时带 feedback 返回起草，最多两次；mood=stop 正常结束。topic 是必填运行输入。请定义严格的审核输出契约，提供 submit 的本地操作示例；进程退出、重新加载 run 后仍能从暂停处推进。不要将 mood 重命名成 decision，也不要把 stop 当执行异常。

交付要求：在这个目录生成 workflow JSON、自定义 executor 配置/程序（如需要）、RUNBOOK.md，以及 submission.json。submission.json 格式为 {"workflow":"相对路径","roles":{逻辑角色:实际节点ID}}，本题逻辑角色是 ["draft", "review", "finalize"]；角色映射只是供测试定位节点，你可以自由选择节点 ID。RUNBOOK.md 说明执行方法、推进方式和已验证/未验证内容。不要调用 Codex/Claude 模型执行交付的流程；模型步骤将由评测器用确定性程序替代。非模型程序可在本地测试。不需要向用户提问，需求已完整给出。只在当前工作目录工作。
