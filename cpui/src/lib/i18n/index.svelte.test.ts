import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { DEFAULT_LOCALE, i18n, LOCALES, t, th, type AnyKey } from './index.svelte';
import { en } from './locales/en';

describe('t', () => {
	it('finds a message', () => {
		expect(t('state.running')).toBe('Running');
	});

	it('fills in the params', () => {
		expect(t('fmt.agoM', { n: 3 })).toBe('3m ago');
	});

	it('picks the plural the count asks for', () => {
		expect(t('ping.cta.reviewFiles', { count: 1 })).toBe('Review 1 file');
		expect(t('ping.cta.reviewFiles', { count: 2 })).toBe('Review 2 files');
		expect(t('ping.cta.reviewFiles', { count: 0 })).toBe('Review 0 files');
	});

	it('formats a count for the locale', () => {
		expect(t('ping.cta.reviewFiles', { count: 1234 })).toBe('Review 1,234 files');
	});

	it('keeps a key with no plural family when given a count', () => {
		expect(t('page.flow.valid', { count: 2 })).toBe('Valid');
	});

	it('answers an unknown key with the key, so the gap shows', () => {
		expect(t('no.such.key' as AnyKey)).toBe('no.such.key');
	});

	it('leaves a placeholder nothing fills', () => {
		expect(t('fmt.agoM', {})).toBe('{n}m ago');
		expect(t('fmt.agoM', { n: null })).toBe('{n}m ago');
		expect(t('fmt.agoM', { n: undefined })).toBe('{n}m ago');
	});

	it('fills in zero, false and an empty string', () => {
		expect(t('fmt.agoM', { n: 0 })).toBe('0m ago');
		expect(t('fmt.agoM', { n: false })).toBe('falsem ago');
		expect(t('fmt.agoM', { n: '' })).toBe('m ago');
	});

	it('does not read a value as another placeholder', () => {
		expect(t('page.flow.cloned', { name: '{name}' })).toBe('Cloned as {name}');
	});
});

describe('th', () => {
	it('keeps the message’s markup and escapes what is put in it', () => {
		expect(th('flow.insp.deleteBody', { id: '<script>"&\'' })).toBe(
			'The stage <b>&#60;script&#62;&#34;&#38;&#39;</b> is removed from this draft.'
		);
	});

	it('answers the message itself with no params', () => {
		expect(th('flow.insp.deleteBody')).toBe(en['flow.insp.deleteBody']);
	});

	it('picks the plural and still escapes the rest', () => {
		expect(th('flow.insp.deleteChainBody', { id: '<x>', count: 1 })).toContain('&#60;x&#62;');
		expect(th('flow.insp.deleteChainBody', { id: 'a', count: 1 })).toBe(
			en['flow.insp.deleteChainBody_one'].replace('{id}', 'a').replace('{count}', '1')
		);
		expect(th('flow.insp.deleteChainBody', { id: 'a', count: 2 })).toBe(
			en['flow.insp.deleteChainBody_other'].replace('{id}', 'a').replace('{count}', '2')
		);
	});
});

describe('the locale', () => {
	beforeEach(() => {
		localStorage.clear();
	});
	afterEach(async () => {
		await i18n.set(DEFAULT_LOCALE, false);
		localStorage.clear();
		vi.unstubAllGlobals();
	});

	it('starts in English, the tag following the locale', () => {
		expect(i18n.locale).toBe('en');
		expect(i18n.tag).toBe('en');
		expect(Object.keys(LOCALES)).toContain(DEFAULT_LOCALE);
	});

	it('sets the document’s language and remembers the choice', async () => {
		await i18n.set('en');
		expect(document.documentElement.lang).toBe('en');
		expect(localStorage.getItem('apothecary.locale')).toBe('en');
	});

	it('does not remember what it was only asked to apply', async () => {
		await i18n.set('en', false);
		expect(localStorage.getItem('apothecary.locale')).toBeNull();
	});

	it('ignores a locale it does not know', async () => {
		await i18n.set('xx' as 'en');
		expect(i18n.locale).toBe('en');
		expect(localStorage.getItem('apothecary.locale')).toBeNull();
	});

	it('survives storage being blocked', async () => {
		vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
			throw new Error('blocked');
		});
		await expect(i18n.set('en')).resolves.toBeUndefined();
		expect(i18n.locale).toBe('en');
	});

	it.each([
		['a saved locale', 'en', ['fr-FR']],
		['a saved locale it no longer has', 'xx', ['en-GB']],
		['the browser’s language', null, ['en-US']],
		['the default, for a language it lacks', null, ['fr-FR']]
	])('boots on %s', async (_, saved, languages) => {
		if (saved) localStorage.setItem('apothecary.locale', saved);
		vi.stubGlobal('navigator', { languages });
		await i18n.init();
		expect(i18n.locale).toBe('en');
		expect(localStorage.getItem('apothecary.locale')).toBe(saved);
	});
});
