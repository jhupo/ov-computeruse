import { dictionaries, type Locale } from "./dictionaries";

export { dictionaries, type Dictionary, type Locale } from "./dictionaries";

export const defaultLocale: Locale = "zh-CN";

export const localeNames: Record<Locale, string> = {
  "zh-CN": "中文",
  "en-US": "English",
};

export function detectInitialLocale(): Locale {
  if (typeof navigator === "undefined") {
    return defaultLocale;
  }
  return navigator.language.toLowerCase().startsWith("zh") ? "zh-CN" : "en-US";
}

export function getDictionary(locale: string | undefined): (typeof dictionaries)[Locale] {
  return dictionaries[toLocale(locale)];
}

export function toLocale(locale: string | undefined): Locale {
  if (locale && locale in dictionaries) {
    return locale as Locale;
  }
  return defaultLocale;
}

export function createTranslator(locale: Locale) {
  return (path: string, params?: Record<string, string | number>) => translate(path, locale, params);
}

export function translate(path: string, locale: string | undefined = defaultLocale, params?: Record<string, string | number>): string {
  const value = path.split(".").reduce<unknown>((current, key) => {
    if (typeof current !== "object" || current === null) {
      return undefined;
    }
    return (current as Record<string, unknown>)[key];
  }, getDictionary(locale));

  if (typeof value !== "string") {
    return path;
  }

  return Object.entries(params ?? {}).reduce((text, [key, replacement]) => {
    return text.split(`{{${key}}}`).join(String(replacement));
  }, value);
}
