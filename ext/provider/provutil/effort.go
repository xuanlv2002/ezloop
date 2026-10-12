/*
effort 是思考强度档位在三协议间的共享归一。
调用方（宿主 UI）提供 Claude Code / Codex 风格的分档：
minimal / low / medium / high / xhigh / max / ultra，另有 none（关闭）
与空串（不控制）。下游各协议只认 low/high/max 三档强度（部分协议
无强度、只认开关），故先经 NormalizeEffort 归一：

	minimal, low            → low
	medium, high, xhigh     → high
	max, ultra              → max
	none                    → none（关闭思考）
	""                      → ""（不控制，请求不带思考字段）
	未知取值                → high（保守）

各协议映射差异（在各自请求侧实现）：
  - openai chat completions：reasoning_effort（协议无 none 档——none
    落到"不发字段"）；
  - anthropic：thinking.type = enabled/disabled（协议无强度档，
    非空档位一律 enabled、none 一律 disabled）；
  - responses：output_config.effort（none 也是合法值，原样下发）。
*/
package provutil

/* NormalizeEffort 把 UI 档位归一为 low/high/max/none/""（见包注释）。 */
func NormalizeEffort(effort string) string {
	switch effort {
	case "", "none":
		return effort
	case "minimal", "low":
		return "low"
	case "max", "ultra":
		return "max"
	case "medium", "high", "xhigh":
		return "high"
	default:
		return "high"
	}
}

/* ApplyEffort 归一后写进目标字符串字段（目标协议不支持 none 时省略）。 */
func ApplyEffort(dst *string, effort string, noneAllowed bool) {
	e := NormalizeEffort(effort)
	if e == "none" && !noneAllowed {
		e = ""
	}
	*dst = e
}
