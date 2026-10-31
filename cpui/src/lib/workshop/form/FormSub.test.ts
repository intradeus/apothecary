import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { text } from '$test/helpers';
import FormSub from './FormSub.svelte';

describe('FormSub', () => {
	it('holds what the section puts in it', () => {
		render(FormSub, { children: text('a group') });
		expect(screen.getByText('a group')).toBeInTheDocument();
	});
});
