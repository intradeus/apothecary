import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it } from 'vitest';
import { prefs } from '$lib/app/prefs.svelte';
import { THEMES } from '$lib/design/themes';
import ThemePicker from './ThemePicker.svelte';

const trigger = () => screen.getByRole('button', { name: 'Change theme' });

beforeEach(() => {
	prefs.theme = 'ember-sky';
});

describe('ThemePicker', () => {
	it('shows the current theme, by name and by its own swatch', () => {
		render(ThemePicker);
		expect(trigger()).toHaveTextContent('Ember × Sky');
		expect(trigger()).toHaveAttribute('title', 'Theme: Ember × Sky');
		expect(trigger().querySelector('[data-theme]')).toHaveAttribute('data-theme', 'ember-sky');
	});

	it('drops the name on the rail and keeps the swatch', () => {
		render(ThemePicker, { rail: true });
		expect(trigger()).not.toHaveTextContent('Ember × Sky');
		expect(trigger().querySelector('[data-theme]')).toHaveAttribute('data-theme', 'ember-sky');
	});

	it('lists every theme, each swatch resolving its own colours through data-theme', async () => {
		render(ThemePicker);
		await userEvent.click(trigger());
		expect(trigger()).toHaveAttribute('aria-expanded', 'true');

		for (const th of THEMES) {
			const row = await screen.findByRole('button', { name: th.label });
			const swatch = row.querySelector('[data-theme]')!;
			expect(swatch).toHaveAttribute('data-theme', th.id);
			expect(swatch.outerHTML).not.toMatch(/style=|#[0-9a-f]{3,8}\b|rgb/i);
		}
	});

	it('applies a pick and closes', async () => {
		render(ThemePicker);
		await userEvent.click(trigger());
		await userEvent.click(await screen.findByRole('button', { name: 'Forest' }));

		expect(prefs.theme).toBe('forest');
		expect(trigger()).toHaveTextContent('Forest');
		await waitFor(() =>
			expect(screen.queryByRole('button', { name: 'Solarized' })).not.toBeInTheDocument()
		);
	});

	it('closes on a second click of the trigger, changing nothing', async () => {
		render(ThemePicker);
		await userEvent.click(trigger());
		await userEvent.click(trigger());
		expect(trigger()).toHaveAttribute('aria-expanded', 'false');
		expect(prefs.theme).toBe('ember-sky');
	});
});
