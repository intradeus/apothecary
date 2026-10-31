import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import Segmented from './Segmented.svelte';

const options = [
	{ value: 'all', label: 'All', count: 12 },
	{ value: 'live', label: 'Live', count: 0, title: 'Running now' },
	{ value: 'done', label: 'Done' }
];
const radio = (name: string | RegExp) => screen.getByRole('radio', { name });
const group = () => screen.getByRole('radiogroup');

describe('Segmented', () => {
	it('is a named radio group with the value checked', () => {
		render(Segmented, { options, value: 'live', label: 'Filter' });
		expect(screen.getByRole('radiogroup', { name: 'Filter' })).toBeInTheDocument();
		expect(radio(/Live/)).toBeChecked();
		expect(radio(/All/)).not.toBeChecked();
	});

	it('is one stop on Tab: only the selected option takes it', () => {
		render(Segmented, { options, value: 'live' });
		expect(radio(/Live/)).toHaveAttribute('tabindex', '0');
		expect(radio(/All/)).toHaveAttribute('tabindex', '-1');
		expect(radio(/Done/)).toHaveAttribute('tabindex', '-1');
	});

	it('chooses on a click', async () => {
		const onchange = vi.fn();
		render(Segmented, { options, value: 'all', onchange });

		await userEvent.click(radio(/Done/));

		expect(onchange).toHaveBeenCalledExactlyOnceWith('done');
		expect(radio(/Done/)).toBeChecked();
		expect(radio(/Done/)).toHaveAttribute('tabindex', '0');
	});

	it.each([
		['{ArrowRight}', 'all', 'live'],
		['{ArrowDown}', 'all', 'live'],
		['{ArrowLeft}', 'live', 'all'],
		['{ArrowUp}', 'live', 'all'],
		['{ArrowRight}', 'done', 'all'],
		['{ArrowLeft}', 'all', 'done'],
		['{Home}', 'done', 'all'],
		['{End}', 'all', 'done']
	])('%s from %s lands on %s and takes the focus', async (key, from, to) => {
		const onchange = vi.fn();
		render(Segmented, { options, value: from, onchange });

		radio(new RegExp(from, 'i')).focus();
		await userEvent.keyboard(key);

		expect(onchange).toHaveBeenCalledExactlyOnceWith(to);
		expect(radio(new RegExp(to, 'i'))).toHaveFocus();
	});

	it('leaves other keys alone', async () => {
		const onchange = vi.fn();
		render(Segmented, { options, value: 'all', onchange });

		radio(/All/).focus();
		await userEvent.keyboard('x');

		expect(onchange).not.toHaveBeenCalled();
	});

	it('takes nothing while disabled, by pointer or by key', async () => {
		const onchange = vi.fn();
		render(Segmented, { options, value: 'all', disabled: true, onchange });

		await userEvent.click(radio(/Done/));
		await fireEvent.keyDown(group(), { key: 'ArrowRight' });

		expect(radio(/Done/)).toBeDisabled();
		expect(group()).toHaveAttribute('aria-disabled', 'true');
		expect(onchange).not.toHaveBeenCalled();
	});

	it('shows a count of zero', () => {
		render(Segmented, { options, value: 'all' });
		expect(radio(/Live/)).toHaveTextContent('Live0');
		expect(radio(/Done/)).toHaveTextContent(/^Done$/);
	});

	it('passes the option title through', () => {
		render(Segmented, { options, value: 'all' });
		expect(screen.getByTitle('Running now')).toBe(radio(/Live/));
	});

	it('tints the label only while its option is selected', async () => {
		const toned = [
			{ value: 'allow', label: 'Allow', tone: 'success' as const },
			{ value: 'block', label: 'Block', tone: 'danger' as const }
		];
		render(Segmented, { options: toned, value: 'allow' });
		expect(radio('Allow').style.color).toBe('var(--success)');
		expect(radio('Block').style.color).toBe('');

		await userEvent.click(radio('Block'));
		expect(radio('Allow').style.color).toBe('');
		expect(radio('Block').style.color).toBe('var(--danger)');
	});

	it('marks a typed option with its dot', () => {
		render(Segmented, {
			options: [{ value: 'a', label: 'Agent', dot: 'running' as const }],
			value: 'a'
		});
		expect(radio('Agent').querySelector('.hue-running')).not.toBeNull();
	});

	it('flattens off the glass when asked', () => {
		render(Segmented, { options, value: 'all', glass: false });
		expect(group()).toHaveClass('flat');
		expect(group()).not.toHaveClass('glass');
	});
});
