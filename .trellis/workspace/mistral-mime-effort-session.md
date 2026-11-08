# Mistral 空 MIME 与最终思考等级策略（2026-11-08）

- 用户要求查截图20:58:48/49两个422、最小修复、少测试并直接部署OVH。对应两条Mistral请求均为第29条user图片带 `image_url.MimeType:""`；前轮合成图零生成schema对照已证实extra_forbidden，不是请求体上限或不支持视觉。
- 54b5b26：仅精确DeepSeek Chat user图片清空内部空/null MimeType，URL-only对象继续转字符串；保留图片、data URL、历史、detail和非空MIME，重复字段本地拒绝。相关回归1.015秒通过。
- 用户继续追加两次要求：89a4cdc先统一缺省/非法档位high、仅low/high/max；3ed0ad8再将max映射high，最终仅low/high保留，其余含省略/null/空值/错误类型均high。根/嵌套effort共用规则；兼容开关不会生成none，预算/未知reasoning结构/重复或归一化后冲突仍拒绝；其他模型/协议/号池不变。对应定向回归1.833秒、1.884秒通过，未跑全仓/race/vet或主动生产推理。
- 三次代码及规格均提交推送master，部署复用既有脚本仅内存适配，472文件逐项SHA256核验；首轮未压缩传输超时后先确认部分源码、旧服务健康且未停服，再以压缩归档补齐。每次精确container/image/revision守卫，一致完整备份后按原Compose仅覆盖image，保留191账号、186条TH注册核心资料、config、routingSecret、Mistral明文注册库及所有原运行参数。
- 三次均Docker healthy/重启0、healthcheck和双内网别名保留，从new-api分别验证两个healthz。最终3ed0ad8于13:17:56 UTC内部验收，13:18:24.177公网登录页/healthz/四资源200且SHA256匹配，验收通过即告知用户；独立浏览器已关闭。89a4cdc被追加的3ed0ad8替代，无单独公网验收。阶段时间/镜像/备份/回退见docs/ovh-deployment.md。
- 只读自然样本截至13:18:25.500（按请求起始ts、已落盘记录）：54b5b26阶段141成功/21客户端取消，89a4cdc阶段32成功/2取消，3ed0ad8初步8成功；各阶段无新Mistral错误事件/422。不是完整10分钟或旧失败请求重放/具体档位生产对照，未声称第三方503根治。
- 无新增业务无关临时文件、pcap、敏感副本或脚本；正式release/新旧镜像/完整备份保留，旧临时产物待同意再清理，Trellis不清理。部署记录文档更新不再次构建/重启。
