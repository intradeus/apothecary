import { render, screen } from '@testing-library/svelte';
import { expect, it } from 'vitest';
import { text } from '$test/helpers';
import Group from './Group.svelte';

it('puts what it says under its caption', () => {
	render(Group, { caption: 'Reads', children: text('The files it opens') });
	const caption = screen.getByText('Reads');
	expect(caption.compareDocumentPosition(screen.getByText('The files it opens'))).toBe(
		Node.DOCUMENT_POSITION_FOLLOWING
	);
});

it('stands as a caption alone', () => {
	render(Group, { caption: 'Reads' });
	expect(screen.getByText('Reads')).toBeInTheDocument();
});
