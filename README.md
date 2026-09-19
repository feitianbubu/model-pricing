# model-pricing

网关 https://api.everai.cloud/ 的**唯一定价源**。每天从 [basellm/llm-metadata](https://github.com/basellm/llm-metadata) 拉取官方真实美元定价并乘记账系数（默认 1.6 = REAL_USD_EXCHANGE_RATE 8 / USDExchangeRate 5），按网关实际上架模型列表过滤，再叠加 `data/overrides.json` 手工维护的条目，输出 `docs/ratio_config.json` 由 GitHub Pages 托管。

改价格只改本仓库；网关在「同步模型定价」里指向：

```
https://<owner>.github.io/model-pricing/ratio_config.json
```

同步落库的值即为记账美元，与本地配置直接可比、可原样应用。

## 数据流

```
basellm(真实USD) ──×1.6──┐
                          ├── ∩ 上架模型(/v1/models) ── + overrides(记账USD 原样) ──→ docs/ratio_config.json
data/overrides.json ──────┘（overrides 不过滤、不缩放）
```

- 上架清单来自 `/v1/models`（MODELS_API_KEY，vip 分组全模型可见）。注意网关会过滤未配价模型：**全新模型先在 overrides 配价 → 同步 → 出现在列表**；或给该 key 用户开启「接受未定价模型」。
- 未覆盖模型每次构建打印清单；任何表达式解析失败会中止构建——宁可数据陈旧，不可价格错误。

## 缩放规则

`ScaleExprPrices` 只缩放表达式里的钱：tier 体内价格系数（`p/c/cr/cc/… * 单价`、`u("…") * 单价`）、常数加项、`fixed()` 金额。量纲无关项不动：`len`/时间等条件阈值、tier 名、`/1000000` 除数、请求规则倍数（`? 6 : 1`）、`image_count` 乘子。

## overrides.json 填写规则

值**已经是记账美元**（不再乘系数）：**官方 CNY 价 ÷ 5**（÷ USDExchangeRate 记账单位，恒定）。用途：

1. 国内厂商按官方 CNY 精确定价（上游预设的国产价折算率混乱，不可信）；
2. video / 任务类模型（上游无此数据），`u()` 的 key 必须匹配网关任务插件的 `usageSchema`；
3. `exclude` 剔除不想出现在预设里的模型。

首批 117 个条目由生产 `/api/ratio_config` 一次性反向生成（2026-09-19），此后以本仓库为准。

## 运行

```bash
go run . -models https://sea.api.everai.cloud/v1/models   # 需 env MODELS_API_KEY
go run .                                              # 不过滤（本地调试）
go test ./...
```

## 部署

1. 仓库 Settings → Secrets → Actions 添加 `MODELS_API_KEY`；
2. Settings → Pages → Source 选 `main` 分支 `/docs` 目录；
3. `.github/workflows/publish.yml` 在 push 与每日 cron（03:23 UTC）再生成并提交 `docs/`；
4. GitHub 会在仓库约 60 天无活动后自动停用 scheduled workflow（发邮件提醒，重新启用即可）。
