// The app's user interface in twenty-eight languages.
//
// English is the reference: i18n/messages/<area>.ts holds one English
// catalog per area of the app, declared with defineMessages, and defines
// that area's keys. Every other language is one file, i18n/locales/<code>.ts,
// exporting a Locale: one catalog per area, typed from the English ones, so
// TypeScript refuses to build when a language lacks a key (or has one English
// does not) and no translation can be forgotten.
//
// Only the language in effect is in memory: the loaders map below imports a
// locale file on demand (Vite emits one chunk per language), state/prefs.ts
// loads the chunk for the language it resolves — before the first render at
// startup, and before switching when the setting changes — and hands it to
// activateLocale. A component reads texts with useT(messages); code outside
// React (toasts, dialogs started from actions) with translate(messages, …).
// Both use the active locale and fall back to English for a key it lacks.
//
// Placeholders are written {name} and filled from the params object.
import { useSyncExternalStore } from "react";
import type { actionsMessages } from "./messages/actions";
import type { runsMessages } from "./messages/runs";
import type { settingsMessages } from "./messages/settings";
import type { shellMessages } from "./messages/shell";
import type { viewsMessages } from "./messages/views";

export type Language =
  | "en"
  | "de"
  | "fr"
  | "it"
  | "id"
  | "cs"
  | "da"
  | "es"
  | "hu"
  | "nl"
  | "nb"
  | "pl"
  | "pt"
  | "ro"
  | "fi"
  | "sv"
  | "vi"
  | "tr"
  | "el"
  | "ru"
  | "uk"
  | "he"
  | "ar"
  | "hi"
  | "ja"
  | "zh-Hans"
  | "zh-Hant"
  | "ko";

/** The languages the app offers, each named in its own language, in the
 * order the settings dialog lists them: English first, then the three the
 * app started with, then the others — Latin script alphabetically by their
 * own name, then Greek, Cyrillic, Hebrew, Arabic, Devanagari and the East
 * Asian scripts. The code is what state/prefs.ts maps the system's BCP 47
 * tag onto ("zh-TW" lands on zh-Hant, "nn" on nb). */
export const LANGUAGES: { value: Language; label: string }[] = [
  { value: "en", label: "English" },
  { value: "de", label: "Deutsch" },
  { value: "fr", label: "Français" },
  { value: "it", label: "Italiano" },
  { value: "id", label: "Bahasa Indonesia" },
  { value: "cs", label: "Čeština" },
  { value: "da", label: "Dansk" },
  { value: "es", label: "Español" },
  { value: "hu", label: "Magyar" },
  { value: "nl", label: "Nederlands" },
  { value: "nb", label: "Norsk bokmål" },
  { value: "pl", label: "Polski" },
  { value: "pt", label: "Português" },
  { value: "ro", label: "Română" },
  { value: "fi", label: "Suomi" },
  { value: "sv", label: "Svenska" },
  { value: "vi", label: "Tiếng Việt" },
  { value: "tr", label: "Türkçe" },
  { value: "el", label: "Ελληνικά" },
  { value: "ru", label: "Русский" },
  { value: "uk", label: "Українська" },
  { value: "he", label: "עברית" },
  { value: "ar", label: "العربية" },
  { value: "hi", label: "हिन्दी" },
  { value: "ja", label: "日本語" },
  { value: "zh-Hans", label: "简体中文" },
  { value: "zh-Hant", label: "繁體中文" },
  { value: "ko", label: "한국어" },
];

/** The languages written right to left; prefs.ts sets the document's
 * direction for them, and editor.css keeps the code editors left to right. */
export const RTL_LANGUAGES: readonly Language[] = ["ar", "he"];

// ---- the catalogs ----

type Catalog<K extends string> = Record<K, string>;

/** The English texts of one area, with the area's name, which is the key
 * the area's catalog has in every Locale. */
export type Messages<A extends Area, K extends string> = { area: A; en: Catalog<K> };

/** The English catalogs by area; the areas and their keys come from here. */
type Catalogs = {
  settings: typeof settingsMessages.en;
  shell: typeof shellMessages.en;
  runs: typeof runsMessages.en;
  views: typeof viewsMessages.en;
  actions: typeof actionsMessages.en;
};
export type Area = keyof Catalogs;

/** One language's texts: a catalog per area with exactly English's keys —
 * in an object literal a key English lacks is an excess property and a key
 * English has is required, so both mistakes fail tsc. */
export type Locale = { [A in Area]: Record<keyof Catalogs[A], string> };

/** Declares the English catalog of one area, see the file comment. */
export function defineMessages<A extends Area, E extends Record<string, string>>(area: A, en: E): Messages<A, Extract<keyof E, string>> {
  return { area, en };
}

// ---- loading ----

/** The loader of each language but English: a dynamic import, so that each
 * one is its own chunk and only the language in effect is ever fetched. */
const loaders: Record<Exclude<Language, "en">, () => Promise<{ locale: Locale }>> = {
  de: () => import("./locales/de"),
  fr: () => import("./locales/fr"),
  it: () => import("./locales/it"),
  id: () => import("./locales/id"),
  cs: () => import("./locales/cs"),
  da: () => import("./locales/da"),
  es: () => import("./locales/es"),
  hu: () => import("./locales/hu"),
  nl: () => import("./locales/nl"),
  nb: () => import("./locales/nb"),
  pl: () => import("./locales/pl"),
  pt: () => import("./locales/pt"),
  ro: () => import("./locales/ro"),
  fi: () => import("./locales/fi"),
  sv: () => import("./locales/sv"),
  vi: () => import("./locales/vi"),
  tr: () => import("./locales/tr"),
  el: () => import("./locales/el"),
  ru: () => import("./locales/ru"),
  uk: () => import("./locales/uk"),
  he: () => import("./locales/he"),
  ar: () => import("./locales/ar"),
  hi: () => import("./locales/hi"),
  ja: () => import("./locales/ja"),
  "zh-Hans": () => import("./locales/zh-Hans"),
  "zh-Hant": () => import("./locales/zh-Hant"),
  ko: () => import("./locales/ko"),
};

/** The texts of one language: null for English (the catalogs in the
 * messages files are the texts), else its loaded Locale. */
export async function loadLocale(lang: Language): Promise<Locale | null> {
  if (lang === "en") return null;
  return (await loaders[lang]()).locale;
}

// The active locale: what useT and translate read. prefs.ts sets it, after
// loading, together with the language in effect.
let active: Locale | null = null;
const listeners = new Set<() => void>();

/** Makes locale (null: English) the one useT and translate read, and
 * re-renders every component that uses useT. */
export function activateLocale(locale: Locale | null) {
  active = locale;
  listeners.forEach((l) => l());
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

function lookup<A extends Area, K extends string>(locale: Locale | null, messages: Messages<A, K>, key: K): string {
  const own = locale ? (locale[messages.area] as Record<string, string>)[key] : undefined;
  return own ?? messages.en[key];
}

export type Params = Record<string, string | number>;

function fill(text: string, params?: Params): string {
  if (!params) return text;
  return text.replace(/\{(\w+)\}/g, (whole, name: string) => (name in params ? String(params[name]) : whole));
}

/** The text for key in the language in effect, for code outside React. */
export function translate<A extends Area, K extends string>(messages: Messages<A, K>, key: K, params?: Params): string {
  return fill(lookup(active, messages, key), params);
}

/** A t(key, params) for one area, re-rendering when the language changes. */
export function useT<A extends Area, K extends string>(messages: Messages<A, K>): (key: K, params?: Params) => string {
  const locale = useSyncExternalStore(subscribe, () => active);
  return (key, params) => fill(lookup(locale, messages, key), params);
}
