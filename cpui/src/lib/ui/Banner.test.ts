import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { text } from '$test/helpers';
import Banner from './Banner.svelte';

describe('Banner', () => {
	it('announces itself as an alert with its title and aside', () => {
		render(Banner, { title: 'Egress held', aside: 'it can wait' });
		const alert = screen.getByRole('alert');
		expect(alert).toHaveTextContent('Egress held');
		expect(alert).toHaveTextContent('it can wait');
	});

	it('lists three items by default and counts the rest', () => {
		render(Banner, { title: 'Held', items: ['a:443', 'b:443', 'c:443', 'd:443', 'e:443'] });
		expect(screen.getByText(/a:443 · b:443 · c:443/)).toBeInTheDocument();
		expect(screen.queryByText(/d:443/)).not.toBeInTheDocument();
		expect(screen.getByText('+2 more')).toBeInTheDocument();
	});

	it('honours maxItems', () => {
		render(Banner, { title: 'Held', items: ['a', 'b', 'c'], maxItems: 1 });
		expect(screen.getByText('+2 more')).toBeInTheDocument();
	});

	it('claims no more when every item fits', () => {
		render(Banner, { title: 'Held', items: ['a', 'b', 'c'] });
		expect(screen.getByText('a · b · c')).toBeInTheDocument();
		expect(screen.queryByText(/more/)).not.toBeInTheDocument();
	});

	it('renders no buttons without a way to act', () => {
		render(Banner, { title: 'Held', action: 'Review' });
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it('needs a label as well as a handler for its action', () => {
		render(Banner, { title: 'Held', onaction: vi.fn(), ondismiss: vi.fn() });
		expect(screen.getAllByRole('button').map((b) => b.textContent)).toEqual(['Dismiss']);
	});

	it('runs the action', async () => {
		const onaction = vi.fn();
		render(Banner, { title: 'Held', action: 'Review', onaction });
		await userEvent.click(screen.getByRole('button', { name: 'Review' }));
		expect(onaction).toHaveBeenCalledOnce();
	});

	it('dismisses without an action beside it', async () => {
		const ondismiss = vi.fn();
		render(Banner, { title: 'Held', ondismiss });
		await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
		expect(ondismiss).toHaveBeenCalledOnce();
		expect(screen.getAllByRole('button')).toHaveLength(1);
	});

	it.each([
		['danger', 'btn-danger-solid'],
		['attention', 'btn-primary']
	] as const)('gives a %s banner its solid button', (tone, cls) => {
		render(Banner, { title: 'Held', tone, action: 'Review', onaction: vi.fn() });
		expect(screen.getByRole('alert')).toHaveClass(tone);
		expect(screen.getByRole('button', { name: 'Review' })).toHaveClass(cls);
	});

	it('is a danger banner unless told otherwise', () => {
		render(Banner, { title: 'Held' });
		expect(screen.getByRole('alert')).toHaveClass('danger');
	});

	it('renders richer content from children', () => {
		render(Banner, { title: 'Held', children: text('Three hosts waiting') });
		expect(screen.getByRole('alert')).toHaveTextContent('Three hosts waiting');
	});
});
