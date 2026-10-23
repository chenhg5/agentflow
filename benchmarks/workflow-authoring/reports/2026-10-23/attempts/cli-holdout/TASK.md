你在一个全新目录中完成下面的实际工作流搭建任务。只使用这个目录里的 ./agentflow 二进制、skill/SKILL.md、fixtures 和标准 Python/Node/shell 工具。不要读取其他目录、源码仓库、历史任务或 golden/reference 答案，不要联网、调用真实模型、发送真实消息。你可以使用工具的 schema/help/validate/dry-run 等能力发现接口。先阅读 skill/SKILL.md，然后完成任务。

业务需求：
给一个农场做纯本地数据工作流。请自己写两个执行器：Python collect 接收运行输入 batch（必填字符串），返回 batch、readings=[3,7,11] 和 meta={unit:'kg',valid:true}；Node.js report 接收这些字段（保持数组、对象、布尔的 JSON 类型），计算 total=21 并返回 batch/total/unit/valid。两个程序都遵循工具的输入输出文件协议，不依赖标准输出传数据。注册两个自定义 executor，设置工作目录和环境变量 FARM_SITE=orchard-2，程序校验环境变量。图从 collect 到 report 再正常结束，提供输入输出 JSON Schema。程序允许用 Python/Node 标准库，交付可运行文件，不要只写伪代码。

交付要求：在这个目录生成 workflow JSON、自定义 executor 配置/程序（如需要）、RUNBOOK.md，以及 submission.json。submission.json 格式为 {"workflow":"相对路径","roles":{逻辑角色:实际节点ID}}，本题逻辑角色是 ["collect", "report"]；角色映射只是供测试定位节点，你可以自由选择节点 ID。RUNBOOK.md 说明执行方法、推进方式和已验证/未验证内容。不要调用 Codex/Claude 模型执行交付的流程；模型步骤将由评测器用确定性程序替代。非模型程序可在本地测试。不需要向用户提问，需求已完整给出。只在当前工作目录工作。
