# 设计与现场契约

## 范围
用户明确只使用第一个邮箱，已注册一个后停止；只要 Agent Pass 月度订阅支付链接。不做余额充值、其他 Pass、自动续费、付款或推理。远端默认分支是 master，本工作区从 fe5fd94 创建。

## 持久化
沿用 AES-GCM signup store，无新增数据库/调度器。Flow 在注册响应后、Key 创建后、邮件验证后调用 Checkpoint；失败返回部分 Result。Retry 不擦除凭据，有 Cookies 时 Resume，不重新 signup；已有 Key 不重复创建。Cookie 重放属性和 Checkout 一并进入原加密信封，Summary 只新增 hasSession/hasCheckout。

接码页是独立 HTTPS 服务，明确选择直连读取；Token Harbor 注册、验证链接和支付请求始终使用该账号原代理，没有代理失败转直连。保留原始注册时间作为邮件筛选起点，避免恢复流程漏掉几分钟前已到达的邮件。

免费模型隐私授权从隐式改为默认关闭（Flow.Config.EnableFreeModels 显式 opt-in）；新注册流程不擅自同意第三方的数据使用条款。

## 官网实际观察（2026-11-05）
- Pricing 的 Choose Agent，月度、Auto-renew 未勾选。
- POST /api/billing/subscribe：plan=starter_pass_monthly, period=monthly, autoRenew=false。
- preview=true 返回 quote：kind=tier, fromTier=free, oneTime=true, period=monthly, planName=Agent Pass, payNowUsd=0.99。
- 常规新购操作返回 checkout_url；真实收银台显示 Token Harbor Agent Pass — Monthly (one-time)，US$0.99。
- 原前端对已有订阅升级有 preview → Confirm and pay 路径，可能直接扣款，不可当作纯链接获取。
- 地区提示由站点要求人工确认，工具不发送 region_acknowledged。

## 支付保护
仅白名单 Agent 月度 Pass；先验证 Free 单次报价（已观测价格上限 $1.99），再固定 expectedPayNowCents 创建托管页面。不会调用余额支付/支付 capture，不自动确认已付费订阅升级。只接受 https://checkout.stripe.com/c/pay/...，不带 URL 用户信息。请求不跟随重定向，失败不回显响应/敏感 URL，不自动重试。

管理员 POST/GET /api/tokenharbor/registrations/{id}/checkout，均 no-store。POST 在上游调用前加密保存占位并将行标为 running；成功落盘后才返回链接，重复选择读原链接。未完成/不确定占位阻止重放，需人工核对官网。周期列表和实时日志不携带链接。UI 只在显式弹窗提供打开和复制操作，不自动打开或付款。

## 真实业务资产
唯一真实账号的会话、Key、链接在本地 .private/ 受限保存；远程对应受限备份目录 /home/ubuntu/backups/tokenharbor-accounts/20261105-agent-pass/。不要把这些业务凭据当作临时文件清理。池中账号仅通过现有管理员 API 增量加入，未付款所以 enabled=false；未覆盖线上配置，没有重建或重启。
