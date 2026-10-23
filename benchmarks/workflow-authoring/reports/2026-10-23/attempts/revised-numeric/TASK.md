你在一个全新目录中完成下面的实际工作流搭建任务。只使用这个目录里的 ./agentflow 二进制、skill/SKILL.md、fixtures 和标准 Python/Node/shell 工具。不要读取其他目录、源码仓库、历史任务或 golden/reference 答案，不要联网、调用真实模型、发送真实消息。你可以使用工具的 schema/help/validate/dry-run 等能力发现接口。先阅读 skill/SKILL.md，然后完成任务。

业务需求：
给工厂质检搭一个纯程序工作流，无需 Agent 提示词。先调用 fixtures/measure.py，其输入是 sample（字符串，来自必填运行输入），输出 score（数字）、defects（字符串数组）、urgent（布尔）。只有 score > 80 且 defects 为空时进入 accept；否则 urgent=true 或 score >= 50 时进入 recheck；其他情况正常结束。accept/recheck 也由自定义程序执行，将 score/defects/urgent 原类型传给它，输出 branch 为 accept/recheck。fixtures/finish.py 可以作为这两个步骤的入口，它从 input.branch 和其他输入生成结果。配置 command、workspace、env，并在 env 里传 QUALITY_SITE=plant-7（finish.py 会检查）。三个程序都是本地程序，任意节点都不应要求 prompt。不要把条件写死在程序里，条件属于图的边。

交付要求：在这个目录生成 workflow JSON、自定义 executor 配置/程序（如需要）、RUNBOOK.md，以及 submission.json。submission.json 格式为 {"workflow":"相对路径","roles":{逻辑角色:实际节点ID}}，本题逻辑角色是 ["measure", "accept", "recheck"]；角色映射只是供测试定位节点，你可以自由选择节点 ID。RUNBOOK.md 说明执行方法、推进方式和已验证/未验证内容。不要调用 Codex/Claude 模型执行交付的流程；模型步骤将由评测器用确定性程序替代。非模型程序可在本地测试。不需要向用户提问，需求已完整给出。只在当前工作目录工作。
