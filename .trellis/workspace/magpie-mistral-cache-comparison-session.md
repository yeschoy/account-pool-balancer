# 2026-11-09 — Magpie / Mistral 缓存实现只读对比

## 用户请求与边界

用户提供聊天截图，朋友称 Magpie→Mistral 有约91%缓存，建议轮流选号和整个会话保持；要求参考实现并深度对比本地改进点。本轮是研究，不改业务实现/线上配置、不部署、不主动推理，不新建实现任务或修改其他任务状态。项目缺少本地Trellis脚本，使用已有规范和本记录归档。

## 固定证据

- 本项目调查基线HEAD `16577f3`，业务含`3ec9611`，起始工作树干净。
- Magpie公开源码固定`b684308c6181aa2304e4c54ab9aac9f8237fbca5`，不是搜索摘要；临时浅克隆在`.analysis/magpie-mistral-cache-20261109/magpie/`。
- 官方Mistral缓存合同重新读取：稳定key/共享前缀，提高机会不保证；64-token块，缓存输入标准价10%。无证据称X-Affinity能保证固定模型节点。
- 邻近NewAPI源码`3914bf70209ecae0fe305ba6a7cdc9bad3fa0949`仅静态补查Claude→Chat的system拼接，不冒充线上精确版本/配置或真实流量。

完整分析、固定源码链接、落地顺序与受控验证方案见`docs/mistral-magpie-cache-comparison.md`。

## 结论

1. 本项目已经有HMAC缓存键、X-Affinity、账号粘性及thinking往返；smart有效绑定先于routes.Order，截图中的rotate+Session不代表每请求换号，也不是新缓存开关。
2. Magpie Mistral preset只声明Chat；缺失key生成的ownCacheKey排除该preset。客户端自带key/按地址添加/extraBody接法不同，不从91%倒推某个功能必然执行。
3. 高价值条件差异：Magpie在Claude→非Claude转换中精确移除每轮变化的计费标记行；本项目实际出站保留。Mock已证实cch变化时即使缓存键/头相同，首system仍不同；无显式session时还会改变fallback身份。当前生产是否含该行未采集，不能称已找到全部生产根因；不全局删动态提示词/思考。
4. 确定性缺口：普通Mistral绑定未跨重启导出，Magpie有受限磁盘记录。建议复用现有metadata，保留隔离/到期/LRU/凭据与账号变更失效，不改严格续传语义。
5. 本项目body prompt_cache_key在标准会话头之前；稳定Session-Id也可能被波动cache key覆盖。TokenHarbor已有session优先做法可借鉴，仅调整Mistral时需迁移与回归，不覆盖调用者上游key。
6. 弱身份首system/user各4096字节会受动态system干扰及长共同开场合并；显式身份覆盖应优先于扩大推断。OpenCode promptCacheKey别名/客户端配置缺口已验证；子agent独立记录与父账号亲和需分层，不能照搬Magpie Auto的全部故障解释。
7. 历史cache-root固定窗口72.422% token加权、48.664%请求均值，以及同账号相同1001消息三次cached256仍有效，数字不是本轮实时测量。身份/持久化不能充分解释稳定进程低命中，不把旧Nginx/旧delete规则再次当现状。
8. 建议先统一分母和最终出站请求诊断，再补确定性工程；必要时经真实推理授权做Magpie/本链路/直连受控热轮比较，不用跨协议/不同workload宣称因果。

## 本地验证

- 本项目通过Go overlay加载临时合成测试：7子用例全部通过（body cache优先、动态system、4096截断、子agent共身份、OpenCode别名、普通绑定不导出、HTTP Mock出站动态行）。断言当前行为，不是修复验收；业务源码没有新增测试文件。
- Magpie临时克隆内4个合成子用例全部通过（Mistral预设ownCacheKey=false及Auto5m、system不参与fallback、长开场后缀区分、主/子affinity分开）。
- Magpie8个既有定向测试通过：重启/磁盘上限、计费行处理及跨协议目标、Mistralthinking/内容转换、多轮前缀与用量、OpenCode驼峰。
- 均Mock/假数据，无真实上游缓存结果；未重复全仓/race/vet/build或生产客户端E2E。研究只提交文档与本Trellis记录，未实现上述建议、不push、不部署。

## 临时文件与完成边界

临时克隆、Go overlay/合成探针、3份测试日志与公开文档浏览器溢出已登记`_tmp_artifacts.md`；没有生产正文/凭据落地。Go测试下载公开依赖到既有共享module/build缓存，不对共享缓存作清理承诺。所有本轮显式保留产物未经同意不清理；本Trellis记录保留。研究完成不等于生产缓存改善已验证。
