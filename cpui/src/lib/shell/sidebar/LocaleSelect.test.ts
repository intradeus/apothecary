import { render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { i18n } from '$lib/i18n/index.svelte';
import type * as I18n from '$lib/i18n/index.svelte';
import { choose } from '$test/fixtures/app.svelte';
import LocaleSelect from './LocaleSelect.svelte';

const extra = vi.hoisted(() => ({ on: false }));

vi.mock('$lib/i18n/index.svelte', async (original) => {
	const real = await original<typeof I18n>();
	return {
		...real,
		get LOCALES() {
			return extra.on
				? { ...real.LOCALES, fr: { label: 'Français', load: real.LOCALES.en.load } }
				: real.LOCALES;
		}
	};
});

afterEach(() => {
	extra.on = false;
});

describe('LocaleSelect', () => {
	it('is not there while there is one language', () => {
		render(LocaleSelect);
		expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
	});

	it('names each language, and switches to the one picked', async () => {
		extra.on = true;
		const set = vi.spyOn(i18n, 'set').mockResolvedValue();
		render(LocaleSelect);

		expect(screen.getByRole('combobox', { name: 'Language' })).toHaveTextContent('English');
		await choose('Language', 'Français');

		await waitFor(() => expect(set).toHaveBeenCalledWith('fr'));
	});

	it('shows the code on the rail', () => {
		extra.on = true;
		render(LocaleSelect, { rail: true });
		expect(screen.getByRole('combobox', { name: 'Language' })).toHaveTextContent('EN');
	});
});
