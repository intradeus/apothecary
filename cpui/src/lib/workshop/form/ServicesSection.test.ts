import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { bindable } from '$test/fixtures/app.svelte';
import { blankService, type ServiceRow } from '../ServicesEditor.svelte';
import ServicesSection from './ServicesSection.svelte';

describe('ServicesSection', () => {
	it('counts the services', () => {
		render(ServicesSection, {
			rows: [blankService('a'), blankService('b')],
			issues: new Map(),
			strict: false
		});
		expect(
			screen.getByRole('heading', { name: 'Declared services' }).nextElementSibling
		).toHaveTextContent('2');
	});

	it('binds the rows through the editor', async () => {
		const props = bindable({ rows: [] as ServiceRow[], issues: new Map(), strict: false });
		render(ServicesSection, props);
		await userEvent.click(screen.getByRole('button', { name: /Add a service/ }));
		expect(props.rows).toHaveLength(1);
	});

	it('passes strict down: an empty start is called out', () => {
		const r = blankService('a');
		render(ServicesSection, { rows: [r], issues: new Map([[r.key, ['start']]]), strict: true });
		expect(screen.getByText(/Start is required/)).toBeInTheDocument();
	});
});
