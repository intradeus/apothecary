import { render, screen } from '@testing-library/svelte';
import { expect, it } from 'vitest';
import { text } from '$test/helpers';
import PanelHead from './PanelHead.svelte';

it('shows what the panel is, its name and what acts on it', () => {
	render(PanelHead, { lead: text('schema'), id: 'plan', children: text('Delete…') });
	for (const part of ['schema', 'plan', 'Delete…'])
		expect(screen.getByText(part)).toBeInTheDocument();
});

it('leaves the name out when the panel has none', () => {
	const { container } = render(PanelHead, { lead: text('main chain') });
	expect(container).toHaveTextContent(/^main chain$/);
});
