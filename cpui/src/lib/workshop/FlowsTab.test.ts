import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { Flow, FlowBinding } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aFlow, aWorkshop } from '$test/fixtures/app.svelte';
import WithHeader from '$test/fixtures/WithHeader.svelte';
import FlowsTab from './FlowsTab.svelte';

vi.mock('$lib/api', () => ({
	api: {
		flows: {
			bindings: vi.fn(),
			create: vi.fn(),
			binding: vi.fn(),
			bind: vi.fn(),
			unbind: vi.fn(),
			copy: vi.fn(),
			copyTemplate: vi.fn()
		},
		integrations: { list: vi.fn() }
	}
}));

const project = aWorkshop({ id: 'p1', name: 'billing', defaultFlow: 'builder' });
const setup = (flows: Flow[] | null = [], over: Record<string, unknown> = {}) => {
	const onreload = vi.fn();
	render(WithHeader, {
		props: { page: FlowsTab, props: { project, flows, error: '', onreload, ...over } }
	});
	return { onreload };
};
const newFlow = () => screen.getAllByRole('button', { name: /New flow/ })[0];

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.flows.bindings).mockResolvedValue([]);
	vi.mocked(api.flows.binding).mockResolvedValue(null as never);
	vi.mocked(api.integrations.list).mockResolvedValue([]);
	fleet.reset();
	toasts.list = [];
});

describe('FlowsTab, listing', () => {
	it('shows a skeleton while loading', () => {
		setup(null);
		expect(screen.queryByText('No flows of its own')).not.toBeInTheDocument();
	});

	it('offers a retry when loading failed', async () => {
		const { onreload } = setup(null, { error: 'down' });
		expect(screen.getByText('Couldn’t load flows')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(onreload).toHaveBeenCalledOnce();
	});

	it('says when the workshop has none of its own', () => {
		setup([]);
		expect(screen.getByText('No flows of its own')).toBeInTheDocument();
	});

	it('sorts its own by name and links them', () => {
		setup([aFlow({ name: 'zeta' }), aFlow({ name: 'alpha' })]);
		const names = screen
			.getAllByRole('link')
			.map((a) => a.getAttribute('href'))
			.filter((h) => h?.includes('/flows/'));
		expect(names[0]).toBe('/w/p1/flows/alpha');
		expect(names.at(-1)).toBe('/w/p1/flows/zeta');
	});

	it('marks the default, and a copy of a template', () => {
		setup([aFlow({ name: 'builder', shadowsBuiltin: true })]);
		expect(screen.getByText('default')).toBeInTheDocument();
		expect(screen.getByText('from template')).toBeInTheDocument();
	});

	it('shows the short hash', () => {
		setup([aFlow({ name: 'x', hash: 'abcdef1234567890' })]);
		expect(screen.getByText('@abcdef12')).toBeInTheDocument();
	});

	it('lists templates apart, viewed not edited', () => {
		setup([aFlow({ name: 'tpl', builtin: true })]);
		expect(screen.getByText('built-in')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'View' })).toHaveAttribute('href', '/w/p1/flows/tpl');
		expect(screen.getByRole('button', { name: 'Copy to edit…' })).toBeInTheDocument();
	});

	it('hides the template section with none', () => {
		setup([aFlow()]);
		expect(screen.queryByText('Built-in templates')).not.toBeInTheDocument();
	});

	it('marks flows this workshop has bound', async () => {
		vi.mocked(api.flows.bindings).mockResolvedValue([{ flowName: 'tpl' } as FlowBinding]);
		setup([aFlow({ name: 'own' }), aFlow({ name: 'tpl', builtin: true })]);
		expect(await screen.findByText('configured')).toBeInTheDocument();
		expect(api.flows.bindings).toHaveBeenCalledWith('p1');
	});

	it('shrugs off a failure to read the bindings', async () => {
		vi.mocked(api.flows.bindings).mockRejectedValue(new Error('down'));
		setup([aFlow()]);
		await waitFor(() => expect(api.flows.bindings).toHaveBeenCalled());
		expect(screen.queryByText('configured')).not.toBeInTheDocument();
	});
});

describe('FlowsTab, a new flow', () => {
	const dialog = async () => {
		await userEvent.click(newFlow());
		return screen.findByRole('dialog', { name: 'New flow' });
	};

	it('creates the skeleton and goes to the editor', async () => {
		vi.mocked(api.flows.create).mockResolvedValue(aFlow());
		setup();
		await dialog();
		await userEvent.type(screen.getByRole('textbox'), 'shiny');
		await userEvent.click(screen.getByRole('button', { name: 'Create & edit' }));

		expect(api.flows.create).toHaveBeenCalledWith(
			'p1',
			'shiny',
			expect.stringContaining('name: shiny')
		);
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/w/p1/flows/shiny'));
		expect(toasts.list.at(-1)?.text).toBe('Flow shiny created');
	});

	it('refuses a name this workshop already has', async () => {
		setup([aFlow({ name: 'taken' })]);
		await dialog();
		await userEvent.type(screen.getByRole('textbox'), 'taken{Enter}');
		expect(api.flows.create).not.toHaveBeenCalled();
	});

	it('shows a refusal in the dialog', async () => {
		vi.mocked(api.flows.create).mockRejectedValue(new Error('bad name'));
		setup();
		await dialog();
		await userEvent.type(screen.getByRole('textbox'), 'shiny');
		await userEvent.click(screen.getByRole('button', { name: 'Create & edit' }));
		expect(await screen.findByText('bad name')).toBeInTheDocument();
		expect(goto).not.toHaveBeenCalled();
	});
});

describe('FlowsTab, configuring', () => {
	it('opens the sheet on the flow', async () => {
		setup([aFlow({ name: 'own' })]);
		await userEvent.click(screen.getByRole('button', { name: 'Configure…' }));
		expect(await screen.findByRole('dialog', { name: 'Configure own' })).toBeInTheDocument();
	});

	it('closes the sheet on cancel', async () => {
		setup([aFlow({ name: 'own' })]);
		await userEvent.click(screen.getByRole('button', { name: 'Configure…' }));
		await screen.findByRole('dialog');
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
	});

	it('confirms a save and refreshes the pills', async () => {
		vi.mocked(api.flows.bind).mockResolvedValue({ params: { a: '1' } } as never);
		setup([aFlow({ name: 'own' })]);
		await userEvent.click(screen.getByRole('button', { name: 'Configure…' }));
		await screen.findByRole('dialog', { name: 'Configure own' });
		await userEvent.click(await screen.findByRole('button', { name: 'Save' }));

		await vi.waitFor(() => expect(api.flows.bind).toHaveBeenCalled());
		await vi.waitFor(() =>
			expect(toasts.list.at(-1)?.text).toBe('own configured for this workshop')
		);
		expect(api.flows.bindings).toHaveBeenCalledTimes(2);
	});

	it('confirms going back to the defaults and refreshes the pills', async () => {
		vi.mocked(api.flows.unbind).mockResolvedValue(undefined as never);
		setup([aFlow({ name: 'own' })]);
		await userEvent.click(screen.getByRole('button', { name: 'Configure…' }));
		await screen.findByRole('dialog', { name: 'Configure own' });
		const reset = await screen.findByRole('button', { name: 'Back to the flow’s defaults' });
		await waitFor(() => expect(reset).toBeEnabled());
		await userEvent.click(reset);

		await vi.waitFor(() =>
			expect(toasts.list.at(-1)?.text).toBe('own is back to its defaults here')
		);
		expect(api.flows.unbind).toHaveBeenCalledWith('p1', 'own');
		expect(api.flows.bindings).toHaveBeenCalledTimes(2);
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
	});
});

describe('FlowsTab, copying', () => {
	it('opens the copy dialog with the fleet to choose from', async () => {
		fleet.projects = [project, aWorkshop({ id: 'p2', name: 'web' })];
		setup([aFlow({ name: 'own' })]);
		await userEvent.click(screen.getByRole('button', { name: /Copy…/ }));
		expect(await screen.findByRole('dialog')).toBeInTheDocument();
	});

	it('takes a template into this workshop', async () => {
		vi.mocked(api.flows.copyTemplate).mockResolvedValue(aFlow({ name: 'tpl', projectId: 'p1' }));
		const { onreload } = setup([aFlow({ name: 'tpl', builtin: true })]);
		await userEvent.click(screen.getByRole('button', { name: 'Copy to edit…' }));
		await userEvent.click(await screen.findByRole('button', { name: 'Copy into workshop' }));

		await waitFor(() => expect(api.flows.copyTemplate).toHaveBeenCalled());
		expect(vi.mocked(api.flows.copyTemplate).mock.calls[0].slice(0, 2)).toEqual(['tpl', 'p1']);
		await waitFor(() => expect(onreload).toHaveBeenCalledOnce());
		expect(goto).toHaveBeenCalledWith('/w/p1/flows/tpl');
		expect(toasts.list.at(-1)?.text).toBe('Copied as tpl into billing');
	});
});
