# 实施顺序
1. 真实UA helper与原生三协议Mock用例。
2. 官网注册限流错误识别、完整Retry-After和提交前钩子，纯本地测试。
3. 全局单预算/间隔/暂停generation及API429前置校验；模拟时钟/短等待测试，不靠长sleep。
4. 固定UI并发与准确提示、Node行为测试；隔离本地浏览器检查表单与Mock暂停后置条件。
5. 全量Go normal/race/vet/build、Node检查、记录spec/验证；仅提交自有修改，其他窗口checkout代码保留。
