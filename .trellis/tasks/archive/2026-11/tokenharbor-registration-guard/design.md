# 设计

- 沿用 encrypted thsignupstore v1；新增 saved 状态、全局 proxies 和每行 subscription 摘要（status/tier/name/checkedAt/error），兼容旧文件。代理池写入和行写入保持同一原子文件；返回代理数量/脱敏标签，不返回原文。
- POST registrations 新增 saveOnly/proxyMode。先完成整批校验与代理分配，再 AddSaved 或 Add；saveOnly 不启动 goroutine。保存行通过现有 retry API 显式转 queued 并执行。旧 autoImport=true 返回明确错误而不是静默入池。
- 原有轮询/设置/调度器不变。代理池独立 GET/PUT /api/tokenharbor/proxies，管理员认证+CSRF、no-store，限制数量并校验每项。不使用浏览器 localStorage 保存代理认证。
- 套餐检测：复用保存 Cookie 和该行固定代理，通过 Flow GET /pricing（不重定向、不重试、响应大小受限、Cache-Control no-cache）。解析官方 Next Flight 中完整的 signedIn/free/plans/currentCycle 组件投影；只认唯一 current 套餐和已知 tier/plan，schema 漂移为未知。官网 current 表示当前有效状态，不从余额/badge/报价推断，也不编造到期时间。
- 真实只读证据（2026-11-05）：既有已验证未付款账号 pricing signedIn=true，free.state=current，三个付费 plans.state=available，currentCycle=null。wallet/live 只有余额字段，不用于订阅判断。无付款/订单创建/推理。
- POST registrations/{rowId}/subscription 更新无凭据摘要；注册完成用同一已保存会话做只读检测，但检测失败不抹掉注册成功。手动 import 使用请求 Context 再检测，仅邮箱 Verified=true 且实时 active paid 才进入原有去重配置更新。
- 不自动清除/禁用旧池账号，避免无授权修改在线调度；本次只约束注册机入池入口，普通手动 Key 导入边界不改变。
