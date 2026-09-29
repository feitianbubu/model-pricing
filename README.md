# model-pricing

new-api 网关的**定价源**。每天从 [basellm/llm-metadata](https://github.com/basellm/llm-metadata) 拉取官方真实美元定价并乘记账系数（默认 1.6 = 真实汇率 8 / USDExchangeRate 5），按 `data/models.json` 白名单过滤，再叠加 `data/overrides.json` 手工维护的条目，生成 `ratio_config.json` 由 GitHub Pages 托管（生成结果不进 git）。

改价格只改本仓库；网关在「同步模型定价」里指向：

```
https://<owner>.github.io/model-pricing/ratio_config.json
```

同步落库的值即为记账美元，与本地配置直接可比、可原样应用。

## 数据流

```
basellm(真实USD) ──×1.6──┐
                          ├── ∩ data/models.json ── + overrides(记账USD 原样) ──→ ratio_config.json
data/overrides.json ──────┘（overrides 不缩放）
```

- `data/models.json` 是发布模型的**唯一**名单，与渠道启用状态无关：上架加一行，下线删一行（原因写在提交信息里）。overrides 里的模型也必须在名单内，否则构建中止。
- 网关同步不会因预设缺某模型而删除本地价格，下线模型删行后网关侧价格保持不变。
- 输出顶层 `model_count` 是去重后的模型总数,打开 Pages 链接即可查看;网关同步只读 `data`,不受影响。
- 名单内未配价的模型每次构建打印清单；任何表达式解析失败会中止构建——宁可数据陈旧，不可价格错误。

## 缩放规则

`ScaleExprPrices` 只缩放表达式里的钱：tier 体内价格系数（`p/c/cr/cc/… * 单价`、`u("…") * 单价`）、常数加项、`fixed()` 金额。量纲无关项不动：`len`/时间等条件阈值、tier 名、`/1000000` 除数、请求规则倍数（`? 6 : 1`）、`image_count` 乘子。

## overrides.json 填写规则

顶层字段的值**已经是记账美元**（不再乘系数）。国内厂商请写进顶层 `cny` 块（结构与顶层相同：`billing_expr` / `billing_mode` / `model_ratio` / `model_price` …），**直接填官方人民币原数**，生成器按 `-cny-rate`（默认 5 = USDExchangeRate）自动 ÷5；同一模型不得同时出现在两处。用途：

1. 国内厂商（阿里/火山/智谱/DeepSeek/MiniMax/Kimi/腾讯/阶跃等）在 `cny` 块按官方人民币精确定价。对话类模型写 `billing_expr` + `billing_mode: tiered_expr` 整套覆盖，不能只写倍率（overrides 按字段覆盖，单写 `model_ratio` 顶不掉上游的表达式）；按次/按秒/按字符计量的模型（TTS/ASR/向量/重排/3D/积分制视频）沿用 `model_price`（元/次）或 `model_ratio`（= 元/百万计量单位 ÷ 2），u() 表达式只有走 JS 任务插件的模型才有用量事实。上游对国内厂商用国际站美元价或 7.3 汇率折算，×1.6 后不等于 CNY÷5，不可信；
2. video / 任务类模型（上游无此数据），`u()` 的 key 必须匹配网关任务插件的 `usageSchema`。

## 运行

```bash
go run .   # 输出 docs/ratio_config.json（已 gitignore）
go test ./...
```

## 部署

1. Settings → Pages → Source 选 `GitHub Actions`；
2. `.github/workflows/publish.yml` 在 push 与每日 cron（03:23 UTC）生成并直接部署到 Pages，不产生提交；
3. GitHub 会在仓库约 60 天无活动后自动停用 scheduled workflow（发邮件提醒，重新启用即可）。
