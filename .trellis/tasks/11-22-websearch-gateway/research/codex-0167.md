# Codex 0.167.0 / Tavily 合同核查（2026-11-22）

本机`codex --version`仅查询版本=0.167.0，未启动子代理或真实Codex推理。

## 一手源码
- https://raw.githubusercontent.com/openai/codex/rust-v0.167.0/codex-rs/core/src/tools/hosted_spec.rs
  - Cached: external_web_access=false；Live: true；Indexed额外indexed_web_access=true。
  - 工具可带filters、user_location、search_context_size，TextAndImage还带search_content_types。
- https://raw.githubusercontent.com/openai/codex/rust-v0.167.0/codex-rs/protocol/src/models.rs
  - WebSearchCall保留可选id/status/action/encrypted_content/internal metadata；Search action字段query/queries；没有sources字段，因此往返可能被合法丢弃。
  - 不可将整个原始public item字节/结构强等判定为历史可信度；应严格校验权威id/status/action.query/queries以及隔离scope，允许缺省非权威sources/null元数据。未知变更/真实动作变更仍失败。
- https://raw.githubusercontent.com/openai/openai-node/master/src/resources/responses/responses.ts
  - WebSearchTool允许web_search和web_search_2025_08_26；external_web_access=false是禁止fetch新外部内容的语义，不能偷偷升级live。
  - url_citation含start_index/end_index/url/title，仅真实已获取来源且在回答中实际出现链接才可生成，不改正文/偏移。
- https://docs.tavily.com/api-reference/endpoint/search
  - include_domains（默认restrict）和exclude_domains；include_usage=true显式要求credits。
  - max_age_hours=-1文档声称never fetch live。用户自定义Hikari代理是否支持/上游实际语义尚未核验，不能仅返回200就承诺cached-only。保持明确拒绝false，直到端到端能力可验证。
  - country是国家boost，不能等价实现精确city/region/timezone。非空location仍拒绝；context_size只是高级上下文空间提示，可明确映射为现有硬上限内的结果条数/片段字节，不冒称官方token数等价。

## 第二阶段范围
接受live true、text-only、基础domain filter、search_context_size提示；strict拒绝cached-only/indexed-only/image/非空地理位置/preview和未知选项。修复Codex允许字段丢失的历史往返；在完整函数item分类后释放顺序屏障，让后续text/reasoning继续逐帧，不等待整轮。生成引用只匹配真实来源在回答中的URL，保留上游已有注解。需要真实客户端与生产账号owner协议的事项仍明确未验收。
