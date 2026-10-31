import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { Validation } from './flowEditor.svelte';
import ValidationPopover from './ValidationPopover.svelte';

// `props:`, since a bare `anchor` would be read as a mount option.
const setup = (validation: Validation | null) =>
	render(ValidationPopover, { props: { anchor: document.body, validation, open: true } });
const ok: Validation = { ok: true, title: 'Valid' };

describe('ValidationPopover', () => {
	it('is checking until there is a result', () => {
		setup(null);
		expect(screen.getByText('Checking…')).toBeInTheDocument();
	});

	it('shows nothing closed', () => {
		render(ValidationPopover, { props: { anchor: document.body, validation: ok, open: false } });
		expect(screen.queryByText('Valid')).not.toBeInTheDocument();
	});

	it('shows the verdict, the stages and the hash', () => {
		setup({ ...ok, stages: 4, hash: 'abcdef0123456789' });
		expect(screen.getByText('Valid')).toBeInTheDocument();
		expect(screen.getByText('4 stages')).toBeInTheDocument();
		expect(screen.getByText('@abcdef012345')).toBeInTheDocument();
	});

	it('counts a stage alone', () => {
		setup({ ...ok, stages: 1 });
		expect(screen.getByText('1 stage')).toBeInTheDocument();
	});

	it('leaves the line out with neither', () => {
		setup(ok);
		expect(screen.queryByText(/stage/)).not.toBeInTheDocument();
	});

	it('lists the errors', () => {
		setup({ ok: false, title: 'Invalid', errors: ['flow:3: unknown key', 'flow:9: no agent'] });
		expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual([
			'flow:3: unknown key',
			'flow:9: no agent'
		]);
	});

	it('calls warnings advisory', () => {
		setup({ ...ok, warnings: ['$model is unbound'] });
		expect(screen.getByText('$model is unbound')).toBeInTheDocument();
		expect(screen.getByText(/advisory; the flow still saves and runs/)).toBeInTheDocument();
	});

	it.each<[string, Validation, string]>([
		['a pass', ok, 'tint-success'],
		['a pass with warnings', { ...ok, warnings: ['w'] }, 'tint-warning'],
		['a failure', { ok: false, title: 'Invalid' }, 'tint-danger']
	])('tones %s', (_, v, tone) => {
		setup(v);
		expect(document.querySelector(`.${tone}`)).not.toBeNull();
	});
});
