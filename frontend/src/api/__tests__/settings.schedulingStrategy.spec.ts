import { describe, expect, it } from "vitest";

import {
  normalizeAccountSchedulingStrategyByPlatformMap,
  SCHEDULING_STRATEGY_PLATFORMS,
} from "../admin/settings";

// 平台级调度策略 map 的归一化不变量：form 侧依赖「全 8 平台对象 + 三值枚举」，
// 这里锁定缺省补全与非法值容错两条防御分支。
describe("normalizeAccountSchedulingStrategyByPlatformMap", () => {
  it("为全部平台补全缺省值 system", () => {
    const result = normalizeAccountSchedulingStrategyByPlatformMap();
    expect(Object.keys(result).sort()).toEqual([...SCHEDULING_STRATEGY_PLATFORMS].sort());
    for (const platform of SCHEDULING_STRATEGY_PLATFORMS) {
      expect(result[platform]).toBe("system");
    }
  });

  it("保留合法覆盖值，未配置平台补 system", () => {
    const result = normalizeAccountSchedulingStrategyByPlatformMap({
      openai: "round_robin",
      anthropic: "default",
    });
    expect(result.openai).toBe("round_robin");
    expect(result.anthropic).toBe("default");
    expect(result.gemini).toBe("system");
    expect(result.antigravity).toBe("system");
    expect(result.grok).toBe("system");
  });

  it("非法/未知值一律归一为 system", () => {
    const result = normalizeAccountSchedulingStrategyByPlatformMap({
      openai: "least_connections",
      gemini: "Round_Robin", // 大小写敏感：非精确匹配的枚举值归 system
      grok: "",
    });
    expect(result.openai).toBe("system");
    expect(result.gemini).toBe("system");
    expect(result.grok).toBe("system");
  });

  it("null/undefined 输入安全返回全 system map", () => {
    expect(
      normalizeAccountSchedulingStrategyByPlatformMap(null).openai,
    ).toBe("system");
  });
});
