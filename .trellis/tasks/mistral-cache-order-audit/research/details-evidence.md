# 自测-test生产详情证据（固定截止UTC2026-11-08 21:00）

## 范围

仅ck_f66b798382a306afd3ec309c / 自测-test，不分析其他Key正文。生产be14987，启动20:24:55 UTC，期间未部署重启。固定窗口82条普通请求；15条wireVersion1详情，UTC20:54:02.620–20:57:24.251。查询时Mistral detailLog=false，未修改其值；历史15条仍保留。

所有请求详情body均被64KiB头尾界限截断，不能恢复中段。真实请求/响应/图片/会话标识只在服务器RAM分析；本地保存的audit.jsonl/identities.jsonl仅包含随机HMAC、长度、布尔、计数、内部/上游追踪ID，无正文/凭据。审计源码和脱敏产物登记_tmp_artifacts.md，Trellis保留。

## 1. 分组，不把同Key等同于相同prompt

用户业务上为同一会话。客户端turn-metadata的session_id/thread_id技术分组却有12组（81条有可提取值、1条31token弱身份请求无值）；每一组均只落到一个账号。该载体用于只读证据比对，不引入新的生产路由身份来源，也不取installation/window之类弱相关值强制合并线程。

- 原截图对应17条，metadata会话/线程/root_turn/context_window稳定，账号zxg-07-vibe-key始终相同；首miss后16hit，token加权61.3553%。旧Session头原值仍为REDACTED，不能对REDACTED做hash冒充原值证明。
- 新详情主组14条，metadata会话/线程/root_turn/context_window值均相同；账号zxg-13-studio-key始终相同；首miss后13hit，input748940/cached516224，加权68.9273%。
- 另一条详情是不同技术线程/不同工具集，input15871/cached15616，不混入主组的前缀因果分析。
- 不将整把Key的多个绑定解释为一个真实Session被随机换号；也不拿新线程首次冷请求解释同线程后续cached=0。

## 2. 客户端与实际上游请求

15/15实际带正文prompt_cache_key及X-Affinity，1个attempt、200、无overflow/switched。缓存值在wire详情中按既有规则脱敏，不能由相同REDACTED推断原值相同；结合已观察到的稳定原始会话头及生产确定性生成路径，未发现键漂移。

对每一条的客户端头尾和实际上游头尾比较：

- 请求头部全部可见部分相等（主组32,716字符）；无额外system插入、首部重序或前缀随机字段。
- 尾部去除已有兼容改写（消费root thinking、追加prompt_cache_key）后，主组可见32,723字符完全相等，工具schema可见尾部字节/属性顺序未改。
- 主组14次实际上游头部及可见工具尾部跨请求始终不变；未出现按对象key随机重排。
- 不能用以上头尾证明截掉的中间每条历史消息；该部分通过完整源码审计及本地HTTP回归验证确定性，而非冒充历史原文已恢复。
- 客户端132795→实际上游132823字节，后续byte delta依次89/122/183/216/249/282/315/348/381/414/447/480/513，符合参数和助手thinking表示的确定性扩展，不是只转发64KiB或任意重新排序。

生产转发保留完整body，详情截断发生在独立preview/capture中；实际约6.58MB请求上游长度仍完整。Mistral不经过其他池的upstreamBody模型map序列化；prepare使用原序RawMessage拼接，messages单向遍历不排序，tools原片段不消费/重编，developer只原位改role；assistant reasoning/图片兼容只改各自消息内容，保留工具调用及顺序。

## 3. 原生usage与低命中

15/15原生响应、适配后响应、普通日志的input/cached逐条一致，均非未知补零。所有保留的响应中X-Max-Retry-Attempts-Reached=false；CF DYNAMIC只是HTTP CDN状态，不等于模型prompt缓存状态。

| UTC | 内部requestId | input | cached | rate | Mistral原生关联ID |
|---|---|---:|---:|---:|---|
| 20:54:12.049 | req_bc0a33fdaf5cc61a75959d91 | 34369 | 31104 | 90.50% | 01a1bcf0-ad9d-7e5c-a375-134d56496817 |
| 20:54:17.676 | req_01f1cfc19e965321fd599b95 | 37608 | 0 | 0% | 01a1bcf0-c399-7ed0-b5db-87a245dc1631 |
| 20:54:28.463 | req_0dfdcd56c1e592fe4238d733 | 45378 | 14592 | 32.16% | 01a1bcf0-edc1-7a01-8607-bbabb10075b6 |
| 20:54:50.992 | req_0b1d36d53198435c244d3bf0 | 50363 | 49024 | 97.34% | 01a1bcf1-45e4-75c0-9281-18d42bc2eb4f |
| 20:56:10.361 | req_9e2baac94fd020b80ae2ea91 | 65385 | 14720 | 22.51% | 01a1bcf2-7c16-75a2-bdb8-3a02c7d5759d |
| 20:56:42.239 | req_0599c4351f53b93119ba966c | 68242 | 66560 | 97.54% | 01a1bcf2-f89c-75f4-98bf-cae41b9f6fe8 |

这次取得的是Mistral-Correlation-Id/X-Kong-Request-Id，是真实服务商追踪ID，不是内部req_*。此前只读普通日志推断“没有上游追踪ID”过宽，应改为“普通摘要漏提取，详情实际已有”。

## 4. 思考/序列化审计

原生SSE的thinking块带closed=true；现有入站reasoning重建没有显式closed。不能直接据此认定bug：官方mistral-common的ThinkChunk.closed默认True，原生响应中也没有证实文本/思考重排导致低命中。未擅自添加字段、丢弃思考或改变history。

参考（仅验证默认值，不冒充托管DeepSeek内部tokenizer合同）：
https://raw.githubusercontent.com/mistralai/mistral-common/main/src/mistral_common/protocol/instruct/chunk.py

JSON对象属性、工具数组、required数组、消息、tool_calls及大数的完整转发稳定性见本地HTTP回归；测试故意使用非字母序工具/属性，避免“原本已排好序所以漏检”。

## 5. 判断与解决边界

未发现账号池造成消息/工具重排序、历史截断、同真实会话换号、缓存头缺失或usage误算。可见生产前缀/工具尾部、完整语义内存采样和源码/HTTP回归相互支持；主要剩余问题仍位于上游缓存复用/上报边界。

真实发现并修复的是Mistral服务商追踪ID漏记：原来仅认X-Request-Id，遗漏Mistral-Correlation-Id和X-Kong-Request-Id。新逻辑Mistral专属、真实ID优先/有界校验、同时记摘要及下游X-Upstream-Request-Id，不改原生头和用量，不改变其他池。

**这不是缓存率修复，不代表生产命中率已提高。** 本轮不盲目排序工具/删上下文、全Key固定号、延TTL、改缓存计数或重启。尚缺同一完整payload直连vs号池的受控生产A/B以及Mistral内部缓存节点/驱逐证据；涉及真实推理费用和用户正文重放，未执行。后续须获授权后用最窄固定样本验证，或用上述服务商ID升级排查。
