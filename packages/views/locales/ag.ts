import type { LocaleResources, SupportedLocale } from "@multica/core/i18n";
import en from "./en/ag.json";
import zhHans from "./zh-Hans/ag.json";
import zhHant from "./zh-Hant/ag.json";
import ko from "./ko/ag.json";
import ja from "./ja/ag.json";
import ru from "./ru/ag.json";
import fr from "./fr/ag.json";
import es from "./es/ag.json";

// The fork's UI strings live in their own `ag` namespace so upstream's locale
// JSON stays untouched. Keys mirror the upstream namespace they sit beside,
// e.g. `issues.swimlane.jump_to_lane`.
const AG: Record<SupportedLocale, Record<string, unknown>> = {
  en,
  "zh-Hans": zhHans,
  "zh-Hant": zhHant,
  ko,
  ja,
  ru,
  fr,
  es,
};

// Each locale entry module (`<locale>.ts`) wraps its resources in this, so the
// namespace reaches both the web's per-locale bundles and the desktop's
// RESOURCES map.
export function withAg(locale: SupportedLocale, resources: LocaleResources): LocaleResources {
  resources.ag = AG[locale];
  return resources;
}
