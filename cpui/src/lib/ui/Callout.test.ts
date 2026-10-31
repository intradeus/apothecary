import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { I } from '$lib/design/icons';
import { text } from '$test/helpers';
import Callout from './Callout.svelte';

describe('Callout', () => {
	it('shows its note behind the info glyph', () => {
		render(Callout, { children: text('This flow is a built-in') });
		expect(screen.getByText('This flow is a built-in')).toBeInTheDocument();
		expect(screen.getByText(I.info)).toBeInTheDocument();
	});

	it('takes another glyph and hue', () => {
		render(Callout, { glyph: I.warn, hue: 'warning', children: text('Note') });
		const glyph = screen.getByText(I.warn);
		expect(glyph.parentElement).toHaveClass('tone-warning');
		expect(screen.queryByText(I.info)).not.toBeInTheDocument();
	});

	it('is brand-toned by default', () => {
		render(Callout, { children: text('Note') });
		expect(screen.getByText(I.info).parentElement).toHaveClass('tone-brand');
	});

	it('cannot be dismissed unless it says how', () => {
		render(Callout, { children: text('Note') });
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it('calls ondismiss from its Dismiss button', async () => {
		const ondismiss = vi.fn();
		render(Callout, { ondismiss, children: text('Note') });
		await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
		expect(ondismiss).toHaveBeenCalledOnce();
	});
});
