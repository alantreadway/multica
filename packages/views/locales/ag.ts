import type { LocaleResources, SupportedLocale } from "@multica/core/i18n";
import en from "./en/ag.json";
import zhHans from "./zh-Hans/ag.json";
import ko from "./ko/ag.json";
import ja from "./ja/ag.json";
import fr from "./fr/ag.json";

// The fork's UI strings live in their own `ag` namespace so upstream's locale
// JSON stays untouched. Keys mirror the upstream namespace they sit beside,
// e.g. `issues.swimlane.jump_to_lane`.
const AG: Record<SupportedLocale, Record<string, unknown>> = {
  en,
  "zh-Hans": zhHans,
  ko,
  ja,
  fr,
};

export function addAgNamespace(resources: Record<SupportedLocale, LocaleResources>): void {
  for (const locale of Object.keys(resources) as SupportedLocale[]) {
    resources[locale].ag = AG[locale];
  }
}
