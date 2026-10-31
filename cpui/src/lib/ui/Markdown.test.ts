import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import Markdown from './Markdown.svelte';

describe('Markdown', () => {
	it('renders markdown', () => {
		render(Markdown, { src: '## Plan\n\n- one\n- **two**' });
		expect(screen.getByRole('heading', { level: 2, name: 'Plan' })).toBeInTheDocument();
		expect(screen.getAllByRole('listitem')).toHaveLength(2);
		expect(screen.getByText('two').tagName).toBe('STRONG');
	});

	// Agent output is semi-trusted.
	it('shows raw HTML as text instead of injecting it', () => {
		const { container } = render(Markdown, { src: '<img src=x onerror="alert(1)">' });
		expect(container.querySelector('img')).not.toBeInTheDocument();
		expect(screen.getByText(/onerror/)).toBeInTheDocument();
	});

	it('keeps a javascript: link’s words and drops the link', () => {
		render(Markdown, { src: '[click me](javascript:alert(1))' });
		expect(screen.getByText('click me')).toBeInTheDocument();
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});

	it('opens a safe link in a new tab', () => {
		render(Markdown, { src: '[docs](https://docs.test)' });
		const link = screen.getByRole('link', { name: 'docs' });
		expect(link).toHaveAttribute('href', 'https://docs.test');
		expect(link).toHaveAttribute('target', '_blank');
		expect(link).toHaveAttribute('rel', 'noreferrer');
	});

	it.each(['doc', 'quiet'] as const)('has a %s form', (form) => {
		const { container } = render(Markdown, { src: 'x', [form]: true });
		expect(container.firstElementChild).toHaveClass('md', form);
	});

	it('updates when the source changes', async () => {
		const { rerender } = render(Markdown, { src: 'first' });
		await rerender({ src: 'second' });
		expect(screen.getByText('second')).toBeInTheDocument();
		expect(screen.queryByText('first')).not.toBeInTheDocument();
	});
});
