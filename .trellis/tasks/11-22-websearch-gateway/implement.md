# 执行计划

1. [x] 独立严格配置、路由与凭据解析；Tavily typed后端，固定来源/禁止重定向/响应上限。
2. [x] Responses 请求工具转换、tool_choice/include校验，原生字节透传。
3. [x] 有界工具循环、真实usage合并、普通函数混用、历史缓存及作用域隔离。
4. [x] JSON/SSE 统一输出、逐帧推进、ID/序号映射与终态失败处理；不支持的函数后文本交错明确失败。
5. [x] 独立命令、优雅关闭、容器/Compose/无凭据配置示例、文档；第四阶段OVH镜像已构建并常驻部署。
6. [x] HTTP Mock矩阵、race/vet/build、私密值审查；更新spec与任务记录，后续仅定向提交；并发窗口误收本任务文件的历史见validation.md。
7. [x] 第二阶段（用户要求继续）：按Codex0.167.0公开hosted_spec.rs/models.rs修复显式external_web_access、往返省略sources/metadata、函数done后流式交错；支持有边界的域名过滤/上下文提示、真实链接引用；补用量/历史回放与安全测试。cached-only尚未保证，仍明确拒绝。
8. [x] 受控真上游合成验证：直接固定一个已授权Mistral账号+Tavily，3次模型请求/1次搜索约5.04秒完成真实流式搜索+引用+Codex形状历史续问。不重放用户历史、不修改线上设置；与Codex Desktop/NewAPI端到端不同。后续守卫仅做本地复验，见validation-phase2.md。
9. [x] 第三阶段09:19 UTC通过现有自测-test Key→正在运行Mistral号池→真实搜索合成验收；3次请求ID关联均单attempt200/同账号/miss-hit-hit，首轮1887in/117out与续问1606in/16out逐项匹配，配置hash未变。
10. [x] 短期加密owner token实现并本地真实HTTP网关→原Pool正常/忙分支、scope/source/TTL/资格/健康/并发/RPM/取消/隐私验证；全仓normal103s/race156s/vet/两个build通过。无header行为不变；第三阶段时线上capability405尚未支持，当时未擅自升级；第四阶段获明确授权后已上线owner v1。
11. [x] 用户回复「好」授权部署：e7eb119已推送，OVH干净archive构建两镜像，保留旧镜像+完整一致备份，沿线上Compose仅覆盖Balancer image；新旧内网别名、健康、配置与公网7JS通过，部署/验收即时反馈。
12. [x] 新增NewAPI私有测试渠道260，不改既有渠道/全局重试/鉴权；固定渠道单attempt，真实搜索和Codex形状历史续问通过，owner start→pinned→start/3轮同号，网关/池/NewAPI用量及计费逐项一致。详见validation-phase4.md。
13. [ ] 真实Codex Desktop/CLI及普通工具混合/来源UI未验收；cached-only仍不支持。业务渠道切换未授权未执行。

验证：go test ./internal/websearch ./internal/searchgateway ./cmd/websearch-gateway；go test -race 同路径；go vet 同路径；go build -o .gotmp/websearch-gateway ./cmd/websearch-gateway。业务构建产物登记 _tmp_artifacts.md，未经同意不清理。

不重放用户历史；真实推理仅阶段2/3明确登记的有界合成测试。不改NewAPI源码或既有业务渠道；第四阶段明确获准升级Balancer、部署网关及新增隔离测试渠道，不在日志/脚本/样例/Git中写用户密钥。第一阶段避开Factory脏改动；第三阶段仅在工作区干净后最小修改proxy.go/server.go/Pool，不触碰Factory业务。
