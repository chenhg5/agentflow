# 工作流搭建 benchmark：2026-10-23 首轮实测

本轮完成 7 次独立冷启动搭建：旧版工具+skill 3 次，新版 3 次，冻结 skill 后再用独立 Codex CLI 完成 1 次留出题。评分器实际运行交付的图与程序，并测试不同返回值；模型和消息渠道在评分时由本地替身模拟。

## 观察结果

| 任务 | 旧版最终功能验收 | 新版最终功能验收 |
| --- | --- | --- |
| 工作日定时提案、交互程序审核、任意 signal 值与两次返工 | 34/34 | 34/34 |
| 工厂质检：数值边界、AND/OR、数组条件、分支优先级与类型传递 | 19/19 | 19/19 |
| 持久暂停、任意 mood 字段、confidence 判断、外部提交与返工 | 37/37 | 37/37 |
| 农场 Python→Node.js 自写执行器、环境变量与嵌套 JSON（留出） | 未测 | 11/11 |

基础题前后都达到最终功能通过 3/3；留出题通过 1/1。**这不代表旧版一次搭对，也不代表成功率从低提升到 100%。** 旧版参与者经过多次探测和修复才完成。新版还会修复自己的验证脚本断言，例如误把 skipped 当 pending；这些不是交付图的执行失败。

另行记录的质量项：旧版 2/3 显式声明了必填运行输入，新版 3/3。旧版 external-loop 为正常耗尽返工增加了一个技术计数节点，题目没有禁止这种实现，因此评分器接受它；新版交付为三节点且从 Codex 开始。这是结构观察，不作为虚假的功能失败计分。

最终工具又对新版三份交付与留出题做了不修改作品的重放，4/4 全部通过。最终工具 SHA-256：`4c8dcc458b5b0555eaa4858d876ddafc299bba1efdcfebc234406d23437bfa8e`。

## 发现并修复的问题

| 发现 | 证据来源 | 改进 |
| --- | --- | --- |
| 空目录运行 schema workflow/executors/trigger 都因缺少仓库 JSON 文件失败 | 三个旧版 Agent 的笔记；独立命令复现 | schema 嵌入二进制，安装后即可自描述 |
| input.* / inputs.* 混淆会静默产生 null，错误延迟到执行时暴露 | 旧版三个交付的探索记录 | skill 解释三个引用上下文；validate 检查静态引用与声明 |
| 触发器 target.inputs 等未知字段被静默丢弃 | 旧版 scheduled-review 的探索记录；独立复现 | 严格解码配置；错误标为 invalid_trigger_config、不可盲目 retry |
| 显式空输入对象在保存/恢复后变为 null，返工时对象契约失败 | golden 自测与独立复现，非 Agent 搭错 | 输入默认空对象；新增恢复循环回归测试 |
| 失败前 history 为 null，Agent 编写的验证器容易崩溃 | 新版 numeric Agent 的验证脚本错误 | CLI 保证 history 返回数组 |
| 公布的 workflow schema 限制了原本受支持的 JSON Schema union 等关键字 | 暴露 schema 后的实现对照 | 契约交给完整 JSON Schema 编译器校验 |
| skill frontmatter 的 version 不在安装校验器支持字段中 | skill-creator 校验 | 版本移到 metadata；实际 skill 校验通过 |

新增 `workflow validate --check-executors`，让没有注册的执行器在交付前就被发现。skill 同时补充了首次循环缺失反馈、节点重入清空旧状态、边次数耗尽语义、严格区分 dry-run 与实跑的指引。业务字段没有加入图核心。

命令级前后复现见 [tool-reproductions.json](tool-reproductions.json)。原始语义评分、工具/skill 指纹和最终工具重放见 [summary.json](summary.json)。

## 评测方式和边界

- 六个应用内参与者均为 `fork_turns=none`，只得到目录中的业务任务、binary、skill 与本地 fixtures。模型继承默认设置，具体模型 ID 未暴露；它们不是六种不同模型。
- 留出题使用全新 Codex CLI 进程，带 ephemeral/ignore-user-config/ignore-rules 参数，没有 resume。没有为它提供历史对话或参考答案。命令记录中没有读取产品源码仓库；这是指令与记录审查，不是 OS 隔离证明。
- CLI 留出会话实际耗时约 69.5 秒。CLI 记录了 usage，但没有可用于可靠跨模型比较的模型 ID；其他会话没有同口径耗时/用量，所以不据此排名。
- 留出题未用于调整 skill，但评测负责人知道题面并编写验收规则；这不是外部盲测数据集。
- 评分器允许节点改名、数组换序、以及符合任务的其他实现。早期评分器曾把正常耗尽循环误判为失败、对非明确要求的首节点加了限制；这些已作为评分规则修正，不计作模型改进。
- 评分器自测通过：4 个正确 golden、1 个等价改名换序变体、7 个故意损坏的变体。持续集成只跑这些确定性测试，不调用模型。
- 本轮是 4 类任务、7 个会话的初始样本；未对多个低能力模型、不同提示表述、不同运行平台和多次重复做统计，不能推断“任意 Agent 都保证成功”。runner 已支持替换任意 CLI 参数数组，后续可沿用相同任务和评分规则。

## 保留的材料

[材料索引](artifacts.json) 包含每次交付位置、原工作目录和本地工具快照索引；`attempts/` 下保留实际作品、说明、验证脚本与压缩命令/Agent 记录。二进制快照位于被 Git 忽略的 results/tool-snapshots，避免把机器二进制提交进源码。归档中的 executors.json/triggers.json 是原 .agentflow 配置的副本，重现作品时放回 .agentflow。

技能旧版与新版分别保留在 snapshots/baseline-skill 和 snapshots/revised-skill。各次 authoring 与后续 replay 使用的工具版本不同，报告保留两种哈希，不把工具重放冒充新的独立搭建。

运行方式见 [benchmark README](../../README.md)。
