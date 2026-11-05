# 设计
- 复用现有原生推理入口与通用UA识别，提取仅提供真实产品兜底的共享helper，不制造客户端身份。
- thRegistrar保留一个容量1的sem；新增nextSignup、pausedUntil与暂停generation。任务进入队列时捕获generation，暂停后旧generation即使期限已过也不会自动提交。
- 注册开始前等待nextSignup以免提前购买验证码；Flow.Config.BeforeSignup在唯一signup POST前复核并记录60秒提交间隔。已有会话恢复不运行新注册钩子。
- thregister.RateLimitError持有已脱敏原因和完整等待时长，满足ErrSessionRejected匹配；加载注册页/预检/提交均识别HTTP限流，提交还识别已实测HTTP200注册过多信息。异常只返回，不做重试。
- API在持久化/Retry之前检查全局暂停，429带向上取整的剩余秒数。已排队worker检查generation后停止，不跨暂停自行复活。失败记录/日志保留明确原因。
- UI固定并发、说明间隔/暂停，沿用既有日志和失败状态，不增加调度系统或存储schema。暂停不跨进程持久化，重启不自动恢复旧任务。
