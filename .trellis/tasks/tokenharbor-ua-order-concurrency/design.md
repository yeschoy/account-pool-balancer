# Design

- 保留 egressid 原 6 项及 For(seed) 的映射；新增命名、不可重新解释的桌面配置（Chrome/Edge × macOS/Windows/Linux × 154–157），新记录将随机 ID 派生的配置名称持久化。所有 TH 会话读写沿用该配置；OAuth 授权仍用真实产品标识，推理 UA 规则不变。新配置采用 reduced UA，平台/品牌和请求类型匹配，不发送未协商高熵提示。24 个新增配置，连同兼容池共 30 种 UA。
- signup store Row/diskRow 增加 browserProfile、completionOrder、verificationMailAttempted、apiKeyCreationAttempted；Summary 只投影非敏感显示字段。新有邮箱账号按随机行 ID 的独立哈希分配两种顺序，无接码页者保留 key-first；旧记录空字段保持旧顺序和原 UA。密码和 GitHub 同用 finish。
- finish 将 key 与邮箱阶段解耦；初始会话 checkpoint 在任何后续动作前，verify-first 验证成功立即 checkpoint 再创建 Key；两种顺序均在 Key POST 前保存 apiKeyCreationAttempted、取得一次性 Key 后立即 checkpoint。不确定 Key 结果不自动重放。OAuth 发信先写 attempted，再请求一次，现有 Key 的旧记录不自动补发。verify-first 超时/拒绝保留会话且停止，不偷偷改顺序。
- 并发配置统一 1–5，API concurrency=0 使用默认；旧持久化 >5 值归一到5，新的 API 越界拒绝。registrar 固定全局5槽、按 attempt 共享有引用计数的任务信号量，活跃任务的限额不重建；最后一个等待/执行者退出才回收任务预算。shutdown/cancel 正确释放。
- task createdAt 使用账号组最早 CreatedAt；生成全部投影后按 createdAt DESC、ID ASC 排序并截断。详情同一时间定义；前端防御排序不修改选择或当前任务。
- 无生产数据迁移/在线配置修改。旧版本可能丢新元数据，部署前需备份（本任务不部署）。

## 资料依据
- Chromium UA reduction: https://www.chromium.org/updates/ua-reduction/ （真实 reduced UA 的 0.0.0、macOS 10_15_7 不是缺少随机细节）。
- Chrome release 2026-11-03: https://chromereleases.googleblog.com/2026/11/stable-channel-update-for-desktop.html （搜索确认157；浏览器读取正文缺少版本，不用来断言具体补丁）。
- Microsoft 官方稳定版发布说明，浏览器读取确认 157.0.4341.42 于2026-11-05发布、156 extended stable：https://learn.microsoft.com/en-us/deployedge/microsoft-edge-relnote-stable-channel 。仅采用已发布大版本，不伪造完整补丁号。
