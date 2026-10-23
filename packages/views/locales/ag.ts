import type { LocaleResources, SupportedLocale } from "@multica/core/i18n";
import en from "./en/ag.json";
import zhHans from "./zh-Hans/ag.json";
import zhHant from "./zh-Hant/ag.json";
import ko from "./ko/ag.json";
import ja from "./ja/ag.json";
import fr from "./fr/ag.json";
import es from "./es/ag.json";
import ru from "./ru/ag.json";

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

// Entry modules call this so the namespace reaches every consumer, including
// the web app's per-locale bundles that bypass `RESOURCES`.
export function withAg(locale: SupportedLocale, resources: LocaleResources): LocaleResources {
  resources.ag = AG[locale];
  return resources;
}
