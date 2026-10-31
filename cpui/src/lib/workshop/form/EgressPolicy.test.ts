import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { bindable } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import EgressPolicy from './EgressPolicy.svelte';

vi.mock('$lib/api', () => ({ api: { egress: { defaults: vi.fn() } } }));

const setup = (over: Record<string, unknown> = {}) => {
	const props = bindable({
		blockedDomains: [] as string[],
		egressMode: 'allow' as 'allow' | 'deny',
		allowDomains: [] as string[],
		denyDomains: [] as string[],
		onerror: vi.fn(),
		...over
	});
	render(EgressPolicy, props);
	return props;
};
const allowBox = () => screen.getByPlaceholderText('api.example.com, *.example.com');
const denyBox = () => screen.getByPlaceholderText('tracker.example.com, *.ads.net');
const defaults = () => screen.getByRole('button', { name: /Add sensible defaults/ });

beforeEach(() => {
	vi.resetAllMocks();
});

describe('EgressPolicy', () => {
	it('edits the allowlist in allow mode', async () => {
		const props = setup();
		await userEvent.type(allowBox(), 'api.test{Enter}');
		expect(props.allowDomains).toEqual(['api.test']);
		expect(screen.getByRole('heading', { name: 'Allowed hosts' })).toBeInTheDocument();
	});

	it('edits the denylist in deny mode', async () => {
		const props = setup({ egressMode: 'deny' });
		await userEvent.type(denyBox(), 'ads.test{Enter}');
		expect(props.denyDomains).toEqual(['ads.test']);
		expect(screen.getByRole('heading', { name: 'Blocked hosts' })).toBeInTheDocument();
	});

	it('flips the mode and keeps both lists', async () => {
		const props = setup({ allowDomains: ['a.test'], denyDomains: ['b.test'] });
		await userEvent.click(screen.getByRole('radio', { name: 'Denylist' }));

		expect(props.egressMode).toBe('deny');
		expect(screen.getByText('b.test')).toBeInTheDocument();
		expect(screen.queryByText('a.test')).not.toBeInTheDocument();
		expect(props.allowDomains).toEqual(['a.test']);

		await userEvent.click(screen.getByRole('radio', { name: 'Allowlist' }));
		expect(screen.getByText('a.test')).toBeInTheDocument();
	});

	it.each(['*.example.com', 'a-b.example.com', 'localhost'])('takes the host %s', async (host) => {
		const props = setup();
		await userEvent.type(allowBox(), `${host}{Enter}`);
		expect(props.allowDomains).toEqual([host]);
	});

	it.each(['http://x.test', '-bad.test', 'a..b', 'x.*'])('refuses %s', async (host) => {
		const props = setup();
		await userEvent.type(allowBox(), `${host}{Enter}`);
		expect(props.allowDomains).toEqual([]);
		expect(screen.getByText(/not a host name/)).toBeInTheDocument();
	});

	it('merges the defaults into the allowlist and says how many were new', async () => {
		vi.mocked(api.egress.defaults).mockResolvedValue(['a.test', 'b.test', 'c.test']);
		const props = setup({ allowDomains: ['a.test'] });
		await userEvent.click(defaults());

		await waitFor(() => expect(props.allowDomains).toEqual(['a.test', 'b.test', 'c.test']));
		expect(screen.getByText('+2 added')).toBeInTheDocument();
	});

	it('says when the defaults were all there', async () => {
		vi.mocked(api.egress.defaults).mockResolvedValue(['a.test']);
		setup({ allowDomains: ['a.test'] });
		await userEvent.click(defaults());
		expect(await screen.findByText('all already present')).toBeInTheDocument();
	});

	it('holds the button while fetching', async () => {
		const load = deferred<string[]>();
		vi.mocked(api.egress.defaults).mockReturnValue(load.promise);
		setup();
		await userEvent.click(defaults());

		expect(screen.getByRole('button', { name: 'Loading…' })).toBeDisabled();
		load.resolve([]);
		await waitFor(() => expect(defaults()).toBeEnabled());
	});

	it("reports a failed fetch through the form's error", async () => {
		vi.mocked(api.egress.defaults).mockRejectedValue(new Error('down'));
		const props = setup();
		await userEvent.click(defaults());
		await waitFor(() => expect(props.onerror).toHaveBeenCalledWith('down'));
		expect(defaults()).toBeEnabled();
	});

	it('has no defaults to add to a denylist', () => {
		setup({ egressMode: 'deny' });
		expect(screen.queryByRole('button', { name: /Add sensible defaults/ })).not.toBeInTheDocument();
	});

	it('shows force-blocked hosts in either mode', () => {
		setup({ blockedDomains: ['evil.test'], egressMode: 'deny' });
		expect(screen.getByText('Force-blocked (1):')).toBeInTheDocument();
		expect(screen.getByText('evil.test')).toBeInTheDocument();
	});

	it('counts the list in use', () => {
		setup({ allowDomains: ['a.test', 'b.test'], denyDomains: ['c.test'] });
		expect(
			screen.getByRole('heading', { name: 'Allowed hosts' }).nextElementSibling
		).toHaveTextContent('2');
	});
});
