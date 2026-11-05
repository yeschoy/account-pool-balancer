# TokenHarbor 正常限速与真实请求标识

- 用户同意正常限速/退避方案；不实施IP、设备、指纹伪装或人工行为模拟。仅本地实现，不部署、不调用真实上游。
- 业务提交`082c03c`：通用/缺省UA使用`account-pool-balancer/tokenharbor-gateway`，真实客户端UA保留，Go自己的产品名称不变；不承诺官网APP分类或回改历史记录。
- 注册固定全局单槽，旧并发参数兼容但不能提高预算；新注册提交至少间隔60秒。先等候再做页面/验证码前置工作，提交前重新检查/记录，既有会话不重新注册。
- 已实测HTTP200注册过多以及429/503等返回结构化暂停，保留完整Retry-After，未给有效时间至少5分钟。暂停generation在API派发时捕获；旧队列结束为明确失败，即使期限过去也不自动恢复。暂停期间register/retry在持久化前返回429，save-only不受影响。
- 全量Go normal/race/vet/build、Node语法/行为检查均通过；本地浏览器验证并发1且readonly。两假账号批量注册时，上游总请求3、signup POST仅1，两条记录均显示暂停、无Key，保留Mock的20分钟退避。
- 暂停与间隔是单进程状态，不保证上游解除限制；队列最长24小时，shutdown可取消。没有增加定时自动重试、注册补号或账号数据迁移。
- 另一个窗口的checkout修复已独立提交0ed063c；本次只提交自有21个文件，不改写其提交。当前未追踪的`.tmp_threbalance/`、`.worktrees/`保留不动。
- 浏览器`th-pacing-mock`关闭，Mock30935停止，54987/54986无监听。临时日志/构建/假数据及两个观察快照登记`_tmp_artifacts.md`，未经用户同意不删除；此Trellis记录与task/spec保留。

## 后续获批推送、部署与连续观察
- 用户明确要求“先推送然后再部署”，并要求观察几分钟。master推送至54f28aaa5b0f77d5713e5feb1fca8bbb74d21a81，以干净提交构建，原Compose仅更新目标服务；未实现随机Claude/Codex/pi身份伪装。
- 构建6.73秒，旧服务退出后完整备份及gzip/SHA256校验0.80秒。68账号、10下游密钥、2导入密钥、81注册记录关键资产/代理池、完整配置和routingSecret均保留；环境/卷/端口/别名/用户/资源相同。正式备份及旧镜像保留，不恢复旧data覆盖新写入。
- 新容器22:14:51.125 UTC启动、22:15:21.718确认healthy。22:15:54–22:18:55按15秒间隔连续13次采样，全200/healthy/revision正确，restartCount=0，未OOM。CPU0.01%–0.84%、内存185.7–236.2MiB含启动缓存，非容量保证。
- 本机和真实未登录公网浏览器healthz/四静态资源均200且哈希匹配，单并发readonly控件确认。启动后日志核验9条既有OpenDesign/Modal请求均200，无新增错误或panic/fatal/保存失败；无真实TokenHarbor推理样本，不主动注册/验证码/订单/付款/推理。
- 已即时反馈上线及观察通过，公网浏览器关闭。部署细节在docs/ovh-deployment.md与远程deploy/observation.json；此后只补记录、不重启。未测精确停机，不误报31秒healthy等待或3分钟观察为停机。
