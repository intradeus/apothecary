import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { anIntegration, bindable, choose } from '$test/fixtures/app.svelte';
import TicketSourceSection, { ticketSourceLabel } from './TicketSourceSection.svelte';

const jira = anIntegration({ id: 'j', name: 'Jira', port: 'ticketsource', kind: 'jira' });
const own = anIntegration({ id: 'o', name: 'Apothecary', port: 'ticketsource', kind: 'internal' });

const setup = (over: Record<string, unknown> = {}) => {
	const props = bindable({ sources: [jira, own], ticketSourceId: 'o', ...over });
	render(TicketSourceSection, props);
	return props;
};
// Each option's label, without the mark the selected one carries.
const options = async () => {
	await userEvent.click(screen.getByRole('combobox', { name: 'Ticket source' }));
	return (await screen.findAllByRole('option')).map((o) =>
		o.querySelector('.truncate')?.textContent?.trim()
	);
};

describe('TicketSourceSection', () => {
	it('lists the built-in tracker first, then the remote ones', async () => {
		setup();
		expect(await options()).toEqual(['Apothecary (built-in tracker)', 'Jira (jira)']);
	});

	it('binds the pick', async () => {
		const props = setup();
		await choose('Ticket source', 'Jira (jira)');
		expect(props.ticketSourceId).toBe('j');
	});

	it('offers the built-in tracker as empty until it exists', async () => {
		const props = setup({ sources: [jira], ticketSourceId: '' });
		expect(await options()).toEqual(['Apothecary (built-in tracker)', 'Jira (jira)']);
		await userEvent.click(screen.getByRole('option', { name: 'Jira (jira)' }));
		expect(props.ticketSourceId).toBe('j');
	});

	it('links to the integrations', () => {
		setup();
		expect(screen.getByRole('link', { name: 'Integrations' })).toHaveAttribute(
			'href',
			'/system/integrations'
		);
	});

	it('keeps a shared workshop’s source, its owner’s, offering none of the caller’s', () => {
		const props = setup({ shared: true, ticketSourceId: 'owners-jira' });
		expect(screen.getByText(/its owner’s ticket source/)).toBeInTheDocument();
		expect(screen.queryByRole('combobox', { name: 'Ticket source' })).toBeNull();
		expect(props.ticketSourceId).toBe('owners-jira');
	});
});

describe('ticketSourceLabel', () => {
	it('names the internal tracker as the built-in one, a remote one by name and kind', () => {
		expect(ticketSourceLabel(own)).toBe('Apothecary (built-in tracker)');
		expect(ticketSourceLabel(jira)).toBe('Jira (jira)');
	});
});
