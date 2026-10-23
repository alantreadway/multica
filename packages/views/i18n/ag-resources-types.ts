import type ag from "../locales/en/ag.json";

// Contributes the fork's `ag` namespace (locales/ag.ts) to the global
// `I18nResources` augmentation in resources-types.ts.
declare global {
  interface I18nResources {
    ag: typeof ag;
  }
}

export {};
