import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Flow, FlowBinding, ParamDecl } from '$lib/domain';
import { aFlow, anIntegration, aWorkshop } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import FlowBindingSheet from './FlowBindingSheet.svelte';

vi.mock('$lib/api', () => ({
	api: {
		flows: { binding: vi.fn(), bind: vi.fn(), unbind: vi.fn() },
		integrations: { list: vi.fn(), models: vi.fn() }
	}
}));

const decl = (over: Partial<ParamDecl> & { name: string }) =>
	({ type: 'string', default: '', known: false, ...over }) as ParamDecl;
const project = aWorkshop({ id: 'p1', harnessIds: ['h1'] });
const flow = (parameters: ParamDecl[] = [decl({ name: 'branch', default: 'main' })]): Flow =>
	aFlow({ name: 'builder', parameters });
const binding = (params: Record<string, string>) => ({ params }) as FlowBinding;

function setup(f: Flow = flow()) {
	const onclose = vi.fn();
	const onsaved = vi.fn();
	render(FlowBindingSheet, { project, flow: f, onclose, onsaved });
	return { onclose, onsaved };
}
const save = () => screen.getByRole('button', { name: 'Save' });
const reset = () => screen.getByRole('button', { name: 'Back to the flow’s defaults' });
const ready = async () => {
	await screen.findByRole('heading', { name: 'Engine parameters' });
};

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.flows.binding).mockResolvedValue(null as never);
	vi.mocked(api.integrations.list).mockResolvedValue([]);
});

describe('FlowBindingSheet', () => {
	it('titles itself and loads the binding', async () => {
		setup();
		expect(screen.getByRole('dialog', { name: 'Configure builder' })).toBeInTheDocument();
		await ready();
		expect(api.flows.binding).toHaveBeenCalledWith('p1', 'builder');
	});

	it('holds its buttons while loading', async () => {
		const load = deferred<FlowBinding>();
		vi.mocked(api.flows.binding).mockReturnValue(load.promise);
		setup();
		expect(save()).toBeDisabled();
		expect(reset()).toBeDisabled();

		load.resolve(binding({}));
		await waitFor(() => expect(save()).toBeEnabled());
	});

	it('says when the flow has no parameters of its own', async () => {
		setup(flow([]));
		expect(
			await screen.findByText('This flow declares no parameters of its own.')
		).toBeInTheDocument();
	});

	it('starts from what is bound and warns of what the flow dropped', async () => {
		vi.mocked(api.flows.binding).mockResolvedValue(
			binding({ branch: 'dev', zeta: '1', alpha: '2' })
		);
		setup();
		await ready();

		expect(screen.getByRole('textbox')).toHaveValue('dev');
		expect(
			screen.getByText('No longer declared by the flow, dropped on save: alpha, zeta')
		).toBeInTheDocument();
	});

	it('saves the values, leaving dropped ones out', async () => {
		vi.mocked(api.flows.binding).mockResolvedValue(binding({ gone: '1' }));
		const saved = binding({ branch: 'dev' });
		vi.mocked(api.flows.bind).mockResolvedValue(saved);
		const { onsaved } = setup();
		await ready();
		await userEvent.type(screen.getByRole('textbox'), 'dev');
		await userEvent.click(save());

		expect(api.flows.bind).toHaveBeenCalledWith('p1', 'builder', { branch: 'dev' });
		expect(onsaved).toHaveBeenCalledWith(saved);
	});

	it('reports nothing bound once all values are gone', async () => {
		vi.mocked(api.flows.bind).mockResolvedValue(binding({}));
		const { onsaved } = setup();
		await ready();
		await userEvent.click(save());
		expect(onsaved).toHaveBeenCalledWith(null);
	});

	it('shows a refusal and stays', async () => {
		vi.mocked(api.flows.bind).mockRejectedValue(new Error('bad value'));
		const { onsaved } = setup();
		await ready();
		await userEvent.click(save());

		expect(await screen.findByText('bad value')).toBeInTheDocument();
		expect(onsaved).not.toHaveBeenCalled();
		expect(save()).toBeEnabled();
	});

	it('goes back to the defaults', async () => {
		vi.mocked(api.flows.unbind).mockResolvedValue(undefined as never);
		const { onsaved } = setup();
		await ready();
		await userEvent.click(reset());

		expect(api.flows.unbind).toHaveBeenCalledWith('p1', 'builder');
		expect(onsaved).toHaveBeenCalledWith(null);
	});

	it('takes nothing bound as already back at the defaults', async () => {
		vi.mocked(api.flows.unbind).mockRejectedValue(new Error('not found'));
		const { onsaved } = setup();
		await ready();
		await userEvent.click(reset());
		await waitFor(() => expect(onsaved).toHaveBeenCalledWith(null));
	});

	it('cancels', async () => {
		const { onclose } = setup();
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(onclose).toHaveBeenCalledOnce();
	});

	it('shows a failure to load', async () => {
		vi.mocked(api.flows.binding).mockRejectedValue(new Error('down'));
		setup();
		expect(await screen.findByText('down')).toBeInTheDocument();
	});

	it('still works when integrations cannot be listed', async () => {
		vi.mocked(api.integrations.list).mockRejectedValue(new Error('down'));
		setup();
		await ready();
		expect(screen.queryByText('down')).not.toBeInTheDocument();
	});

	it("suggests each harness's models, asking once per kind, only for this workshop's harnesses", async () => {
		vi.mocked(api.integrations.list).mockResolvedValue([
			anIntegration({ id: 'h1', name: 'Claude', port: 'harness', kind: 'claude' }),
			anIntegration({ id: 'h2', name: 'Other', port: 'harness', kind: 'codex' })
		]);
		vi.mocked(api.integrations.models).mockResolvedValue([]);
		vi.mocked(api.flows.binding).mockResolvedValue(binding({ harness: 'claude' }));
		setup(
			flow([
				decl({ name: 'harness', known: true, role: 'harness' }),
				decl({ name: 'model', known: true, role: 'model', harness: '$harness' })
			])
		);
		await ready();
		await waitFor(() => expect(api.integrations.models).toHaveBeenCalledWith('h1'));
		expect(api.integrations.models).toHaveBeenCalledOnce();
	});
});
