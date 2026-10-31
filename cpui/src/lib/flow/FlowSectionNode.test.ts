import { render, screen } from '@testing-library/svelte';
import type { NodeProps } from '@xyflow/svelte';
import { expect, it } from 'vitest';
import FlowSectionNode from './FlowSectionNode.svelte';

it('heads a section, saying what it holds on hover', () => {
	const data = { label: 'watchers', title: 'What runs while the run is open' };
	render(FlowSectionNode, { data } as unknown as NodeProps);
	expect(screen.getByText('watchers')).toHaveAttribute('title', data.title);
});
