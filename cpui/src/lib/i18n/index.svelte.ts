// Internationalisation: a tiny, dependency-free message lookup.
//
//   t('run.header.title', { id })          → "New run from {id}"
//   t('runs.count', { count: 3 })          → picks runs.count_one / runs.count_other
//   th('x.bold', { name })                 → same, params HTML-escaped, for {@html}
//
// Keys are typed against the English catalog (the source of truth), so a typo
// or a missing string is a svelte-check error. Other locales are partial: a key
// they lack falls back to English. The active locale is reactive ($state), so
// every `t()` call inside a component or $derived re-renders on a switch.
// Outside a reactive context (module-level constants) call `t()` lazily — in a
// function — never at import time, or the string is frozen in one language.

import { en, type MessageKey } from './locales/en';

export type { MessageKey };
/** A plural family `k_one`/`k_other` is addressed by its base key `k`. */
type PluralBase<K> = K extends `${infer B}_other` ? B : never;
export type AnyKey = MessageKey | PluralBase<MessageKey>;
export type Params = Record<string, string | number | boolean | null | undefined>;
type Catalog = Partial<Record<MessageKey, string>>;

/** Locale registry: add a locale by adding a loader here and its label. */
export const LOCALES = {
	en: { label: 'English', load: async (): Promise<Catalog> => en }
	// fr: { label: 'Français', load: async () => (await import('./locales/fr')).fr }
} as const satisfies Record<string, { label: string; load: () => Promise<Catalog> }>;

export type Locale = keyof typeof LOCALES;
export const DEFAULT_LOCALE: Locale = 'en';
const KEY = 'apothecary.locale';

const isLocale = (v: unknown): v is Locale => typeof v === 'string' && v in LOCALES;

const read = (): string | null => {
	try {
		return localStorage.getItem(KEY);
	} catch {
		return null;
	}
};

const detect = (): Locale => {
	const saved = read();
	if (isLocale(saved)) return saved;
	const nav = typeof navigator === 'undefined' ? [] : navigator.languages;
	for (const l of nav) {
		const base = l.toLowerCase().split('-')[0];
		if (isLocale(base)) return base;
	}
	return DEFAULT_LOCALE;
};

class I18n {
	locale = $state<Locale>(DEFAULT_LOCALE);
	/** The active catalog; English until a non-default locale finishes loading. */
	private cat = $state<Catalog>(en);

	/** Resolve the saved/browser locale and load it. Call once at boot. */
	async init() {
		await this.set(detect(), false);
	}

	async set(locale: Locale, persist = true) {
		if (!isLocale(locale)) return;
		this.cat = await LOCALES[locale].load();
		this.locale = locale;
		if (typeof document !== 'undefined') document.documentElement.lang = locale;
		if (persist) {
			try {
				localStorage.setItem(KEY, locale);
			} catch {
				/* storage blocked: the choice lasts this tab only */
			}
		}
	}

	/** BCP-47 tag for Intl formatters (dates, numbers, plural rules). */
	get tag(): string {
		return this.locale;
	}

	lookup(key: AnyKey, params?: Params): string {
		let msg: string | undefined;
		if (params && typeof params.count === 'number') {
			const cat = new Intl.PluralRules(this.tag).select(params.count);
			const k = `${key}_${cat}` as MessageKey;
			const other = `${key}_other` as MessageKey;
			msg = this.cat[k] ?? en[k] ?? this.cat[other] ?? en[other];
		}
		msg ??= this.cat[key as MessageKey] ?? en[key as MessageKey] ?? key;
		return params ? interpolate(msg, params) : msg;
	}
}

const interpolate = (msg: string, params: Params, escape = (s: string) => s) =>
	msg.replace(/\{(\w+)\}/g, (m, name: string) => {
		const v = params[name];
		if (v === undefined || v === null) return m;
		return escape(typeof v === 'number' ? v.toLocaleString(i18n.tag) : String(v));
	});

const escapeHtml = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

export const i18n = new I18n();

/** Translate. `count` in params selects the `_one` / `_other` (CLDR) variant. */
export const t = (key: AnyKey, params?: Params): string => i18n.lookup(key, params);

/** Translate for `{@html}`: the message may carry markup (<b>, <code>), params are escaped. */
export const th = (key: AnyKey, params?: Params): string => {
	const msg = t(key);
	if (!params) return msg;
	const withCount =
		typeof params.count === 'number' ? i18n.lookup(key, { count: params.count }) : msg;
	return interpolate(withCount, params, escapeHtml);
};
