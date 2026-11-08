# 实施与验证

1. quota/mistral.go 增加读取/类型验证/刷新；Snapshot 接入深拷贝与路由，保留 source guard。
2. admin account projection 和 refresh-quota 接入套餐 freshness/部分失败语义；UI 替换硬编码未知。
3. 添加 mock 的两类预算、缺字段、权限/HTTP/网络/重定向、旧值保留、来源变化/晚返回、metadata往返及手动刷新测试；Node 验证金额/历史/转义/跨类型。
4. 真实本地隔离假数据验收按钮→后端→落盘→页面，关闭本轮进程；构建与验收产物登记 _tmp_artifacts.md，未经同意不清理。
5. 更新 docs/spec，提交业务修改及所有 Trellis 记录；不擅自推送部署。
