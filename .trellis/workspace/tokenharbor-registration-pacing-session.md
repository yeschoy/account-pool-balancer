# TokenHarbor 正常限速与真实请求标识

- 用户同意正常限速/退避方案；不实施IP、设备、指纹伪装或人工行为模拟。仅本地实现，不部署、不调用真实上游。
- 业务提交`082c03c`：通用/缺省UA使用`account-pool-balancer/tokenharbor-gateway`，真实客户端UA保留，Go自己的产品名称不变；不承诺官网APP分类或回改历史记录。
- 注册固定全局单槽，旧并发参数兼容但不能提高预算；新注册提交至少间隔60秒。先等候再做页面/验证码前置工作，提交前重新检查/记录，既有会话不重新注册。
- 已实测HTTP200注册过多以及429/503等返回结构化暂停，保留完整Retry-After，未给有效时间至少5分钟。暂停generation在API派发时捕获；旧队列结束为明确失败，即使期限过去也不自动恢复。暂停期间register/retry在持久化前返回429，save-only不受影响。
- 全量Go normal/race/vet/build、Node语法/行为检查均通过；本地浏览器验证并发1且readonly。两假账号批量注册时，上游总请求3、signup POST仅1，两条记录均显示暂停、无Key，保留Mock的20分钟退避。
- 暂停与间隔是单进程状态，不保证上游解除限制；队列最长24小时，shutdown可取消。没有增加定时自动重试、注册补号或账号数据迁移。
- 另一个窗口的checkout修复已独立提交0ed063c；本次只提交自有21个文件，不改写其提交。当前未追踪的`.tmp_threbalance/`、`.worktrees/`保留不动。
- 浏览器`th-pacing-mock`关闭，Mock30935停止，54987/54986无监听。临时日志/构建/假数据及两个观察快照登记`_tmp_artifacts.md`，未经用户同意不删除；此Trellis记录与task/spec保留。
