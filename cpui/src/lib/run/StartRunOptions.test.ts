import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { flushSync } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Flow, Integration, Model, OpenMergeRequest, SkillsetRef } from '$lib/domain';
import { aProject } from '$test/fixtures';
import { aFlow, anOpenMr, aParam, bound, settled } from '$test/fixtures/run-components';
import { deferred } from '$test/helpers';
import StartRunOptions from './StartRunOptions.svelte';

vi.mock('$lib/api', () => ({
	api: {
		flows: { get: vi.fn(), binding: vi.fn() },
		projects: { get: vi.fn(), mergeRequests: vi.fn() },
		integrations: { list: vi.fn(), models: vi.fn() },
		skillsets: { list: vi.fn(), versions: vi.fn() }
	}
}));

const defaults = {
	projectId: 'p1',
	flowName: 'builder',
	params: {} as Record<string, string>,
	mr: 0,
	skillsets: [] as SkillsetRef[]
};

type Props = Omit<typeof defaults, 'params'> & {
	params: Record<string, string>;
	askMR?: boolean;
	askSkillsets?: boolean;
	under?: Record<string, string>;
	underLabel?: 'workshop' | 'run';
};

function mount(over: Partial<Props> = {}) {
	const props = bound<Props>({ ...defaults, ...over });
	render(StartRunOptions, { props });
	return props;
}
const flowIs = (over: Partial<Flow>) => vi.mocked(api.flows.get).mockResolvedValue(aFlow(over));
const openMrs = (...list: OpenMergeRequest[]) =>
	vi
		.mocked(api.projects.mergeRequests)
		.mockResolvedValue(list.map((mr) => ({ mr, mine: false, runs: 0 })));
const onMrFlow = { operatesOn: 'merge_request' } as Partial<Flow>;
const advanced = () => screen.findByText(/Advanced · parameters/);
const harness = (id: string, kind: string) =>
	({ id, kind, name: id, port: 'harness' }) as unknown as Integration;

describe('StartRunOptions', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		flowIs({});
		vi.mocked(api.flows.binding).mockResolvedValue({
			projectId: 'p1',
			flowName: 'builder',
			params: {},
			updatedAt: ''
		});
		vi.mocked(api.projects.get).mockResolvedValue(aProject({ harnessIds: [] }));
		vi.mocked(api.projects.mergeRequests).mockResolvedValue([]);
		vi.mocked(api.integrations.list).mockResolvedValue([]);
		vi.mocked(api.integrations.models).mockResolvedValue([]);
		vi.mocked(api.skillsets.list).mockResolvedValue([]);
		vi.mocked(api.skillsets.versions).mockResolvedValue([]);
	});

	describe('loading the flow', () => {
		it('shows nothing until the flow is known', () => {
			vi.mocked(api.flows.get).mockReturnValue(deferred<Flow>().promise);
			const { container } = render(StartRunOptions, { props: bound({ ...defaults }) });

			expect(container).toHaveTextContent('');
		});

		it('asks for the flow it is given', async () => {
			mount({ flowName: 'hotfix' });

			await advanced();
			expect(api.flows.get).toHaveBeenCalledExactlyOnceWith('p1', 'hotfix');
			expect(api.flows.binding).toHaveBeenCalledExactlyOnceWith('p1', 'hotfix');
		});

		it('asks nothing without a project or a flow', () => {
			mount({ flowName: '' });
			expect(api.flows.get).not.toHaveBeenCalled();
		});

		it('says why when the flow cannot be read', async () => {
			vi.mocked(api.flows.get).mockRejectedValue(new Error('gone'));
			mount();

			expect(
				await screen.findByText('Couldn’t load the flow’s parameters: gone')
			).toBeInTheDocument();
		});

		it('clears that once a flow loads', async () => {
			vi.mocked(api.flows.get).mockRejectedValueOnce(new Error('gone'));
			const props = mount();
			await screen.findByText(/Couldn’t load/);

			flushSync(() => (props.flowName = 'hotfix'));

			await advanced();
			expect(screen.queryByText(/Couldn’t load/)).not.toBeInTheDocument();

			flushSync(() => (props.flowName = ''));
			await settled();
			expect(screen.queryByText(/Couldn’t load/)).not.toBeInTheDocument();
		});

		it('ignores the answer for a flow no longer chosen', async () => {
			const slow = deferred<Flow>();
			vi.mocked(api.flows.get).mockImplementation((_, name) =>
				name === 'slow'
					? slow.promise
					: Promise.resolve(aFlow({ parameters: [aParam({ name: 'fresh' })] }))
			);
			const props = mount({ flowName: 'slow' });

			flushSync(() => (props.flowName = 'quick'));
			await screen.findByLabelText(/^fresh/);

			slow.resolve(aFlow({ parameters: [aParam({ name: 'stale' })] }));
			await settled();

			expect(screen.getByLabelText(/^fresh/)).toBeInTheDocument();
			expect(screen.queryByLabelText(/^stale/)).not.toBeInTheDocument();
		});

		it('ignores the failure of a flow no longer chosen', async () => {
			const slow = deferred<Flow>();
			vi.mocked(api.flows.get).mockImplementation((_, name) =>
				name === 'slow' ? slow.promise : Promise.resolve(aFlow())
			);
			const props = mount({ flowName: 'slow' });

			flushSync(() => (props.flowName = 'quick'));
			await advanced();

			slow.reject(new Error('gone'));
			await settled();

			expect(screen.getByText(/Advanced/)).toBeInTheDocument();
			expect(screen.queryByText(/Couldn’t load/)).not.toBeInTheDocument();
		});
	});

	describe('the layer underneath', () => {
		const depth = { parameters: [aParam({ name: 'depth', default: 'shallow' })] };

		it('reads the workshop’s binding', async () => {
			flowIs(depth);
			vi.mocked(api.flows.binding).mockResolvedValue({
				projectId: 'p1',
				flowName: 'builder',
				params: { depth: 'deep' },
				updatedAt: ''
			});
			mount();

			expect(await screen.findByText('workshop: deep')).toBeInTheDocument();
		});

		it('falls back to the flow’s default when the workshop binds nothing', async () => {
			flowIs(depth);
			mount();
			expect(await screen.findByText('flow default: shallow')).toBeInTheDocument();
		});

		it('falls back to the flow’s default when the binding cannot be read', async () => {
			flowIs(depth);
			vi.mocked(api.flows.binding).mockRejectedValue(new Error('nope'));
			mount();
			expect(await screen.findByText('flow default: shallow')).toBeInTheDocument();
		});

		it('takes the layer it is given and loads none', async () => {
			flowIs(depth);
			mount({ under: { depth: 'medium' }, underLabel: 'run' });

			expect(await screen.findByText('this run: medium')).toBeInTheDocument();
			expect(api.flows.binding).not.toHaveBeenCalled();
		});

		it('explains the layer in its help', async () => {
			mount({ under: {}, underLabel: 'run' });
			expect(
				await screen.findByText(/Override what the run you are starting from used/)
			).toBeInTheDocument();
		});

		it('explains the workshop’s layer by default', async () => {
			mount();
			expect(await screen.findByText(/Override the workshop’s values/)).toBeInTheDocument();
		});
	});

	describe('parameters', () => {
		it('lists the engine’s parameters first, then the flow’s own', async () => {
			flowIs({
				parameters: [
					aParam({ name: 'depth' }),
					aParam({ name: 'max_turns', type: 'int', known: true, default: '10' }),
					aParam({ name: 'timeout', known: true, default: '1h' })
				]
			});
			const { container } = render(StartRunOptions, { props: bound({ ...defaults }) });
			await advanced();

			const names = [...container.querySelectorAll('.pf label .mono')].map((n) => n.textContent);
			expect(names).toEqual(['timeout', 'max_turns', 'depth']);
		});

		it('sets an override and counts it', async () => {
			flowIs({ parameters: [aParam({ name: 'depth' })] });
			const props = mount();

			await userEvent.type(await screen.findByLabelText(/^depth/), 'deep');

			expect(props.params).toEqual({ depth: 'deep' });
			expect(screen.getByText('1 overridden')).toBeInTheDocument();
		});

		it('drops an override emptied again: empty values never travel', async () => {
			flowIs({ parameters: [aParam({ name: 'depth' })] });
			const props = mount();
			const field = await screen.findByLabelText(/^depth/);

			await userEvent.type(field, 'd');
			await userEvent.clear(field);

			expect(props.params).toEqual({});
			expect(screen.queryByText(/overridden/)).not.toBeInTheDocument();
		});

		it('treats blanks as empty', async () => {
			flowIs({ parameters: [aParam({ name: 'depth' })] });
			const props = mount();

			await userEvent.type(await screen.findByLabelText(/^depth/), '   ');

			expect(props.params).toEqual({});
		});

		it('keeps overrides of parameters the flow declares', async () => {
			flowIs({ parameters: [aParam({ name: 'depth' })] });
			const props = mount({ params: { depth: 'deep' } });

			await advanced();

			expect(props.params).toEqual({ depth: 'deep' });
			expect(screen.getByLabelText(/^depth/)).toHaveValue('deep');
			expect(screen.getByText('1 overridden')).toBeInTheDocument();
		});

		it('drops overrides the chosen flow does not declare', async () => {
			flowIs({ parameters: [aParam({ name: 'depth' })] });
			const props = mount({ params: { depth: 'deep', old: 'x' } });

			await advanced();

			await vi.waitFor(() => expect(props.params).toEqual({ depth: 'deep' }));
		});

		it('re-checks them when the flow changes', async () => {
			flowIs({ parameters: [aParam({ name: 'depth' })] });
			const props = mount({ params: { depth: 'deep' } });
			await advanced();

			flowIs({ parameters: [aParam({ name: 'other' })] });
			flushSync(() => (props.flowName = 'hotfix'));

			await vi.waitFor(() => expect(props.params).toEqual({}));
		});
	});

	describe('the merge request', () => {
		it('does not come up for a flow started on a ticket', async () => {
			mount();
			await advanced();

			expect(screen.queryByText('Merge request')).not.toBeInTheDocument();
			expect(api.projects.mergeRequests).not.toHaveBeenCalled();
		});

		it('leaves the choice to an automation', async () => {
			flowIs(onMrFlow);
			mount({ askMR: false });
			await advanced();

			expect(screen.queryByText('Merge request')).not.toBeInTheDocument();
			expect(api.projects.mergeRequests).not.toHaveBeenCalled();
		});

		it('says it is listing them', async () => {
			flowIs(onMrFlow);
			vi.mocked(api.projects.mergeRequests).mockReturnValue(deferred<never>().promise);
			mount();

			expect(await screen.findByText('Listing open merge requests…')).toBeInTheDocument();
		});

		it('starts on the first one open', async () => {
			flowIs(onMrFlow);
			openMrs(anOpenMr({ iid: 20 }), anOpenMr({ iid: 21 }));
			const props = mount();

			expect(await screen.findByRole('combobox', { name: 'Merge request' })).toHaveTextContent(
				'!20 · Add idempotency keys — ada'
			);
			expect(props.mr).toBe(20);
		});

		it('marks a draft and a fork', async () => {
			flowIs(onMrFlow);
			openMrs(anOpenMr({ iid: 20, draft: true, fork: true }));
			mount();

			expect(await screen.findByRole('combobox', { name: 'Merge request' })).toHaveTextContent(
				'!20 · Add idempotency keys — ada · draft · from a fork'
			);
		});

		it('binds the one picked, as a number', async () => {
			flowIs(onMrFlow);
			openMrs(anOpenMr({ iid: 20 }), anOpenMr({ iid: 21, title: 'Other' }));
			const props = mount();

			await userEvent.click(await screen.findByRole('combobox', { name: 'Merge request' }));
			await userEvent.click(screen.getByRole('option', { name: /!21/ }));

			expect(props.mr).toBe(21);
		});

		it('says there are none, and starts on none', async () => {
			flowIs(onMrFlow);
			const props = mount();

			expect(
				await screen.findByText('The repository has no open merge request to start on.')
			).toBeInTheDocument();
			expect(props.mr).toBe(0);
		});

		it('shows why they cannot be listed', async () => {
			flowIs(onMrFlow);
			vi.mocked(api.projects.mergeRequests).mockRejectedValue(new Error('host down'));
			mount();

			expect(await screen.findByText('host down')).toBeInTheDocument();
			expect(screen.queryByRole('combobox', { name: 'Merge request' })).not.toBeInTheDocument();
		});

		it('lets go of it when the flow is no longer started on one', async () => {
			flowIs(onMrFlow);
			openMrs(anOpenMr({ iid: 20 }));
			const props = mount();
			await screen.findByRole('combobox', { name: 'Merge request' });

			flowIs({});
			flushSync(() => (props.flowName = 'hotfix'));

			await vi.waitFor(() => expect(props.mr).toBe(0));
			await waitFor(() => expect(screen.queryByText('Merge request')).not.toBeInTheDocument());
		});

		it('ignores a list that arrives after the flow changed', async () => {
			const late = deferred<never>();
			flowIs(onMrFlow);
			vi.mocked(api.projects.mergeRequests).mockReturnValue(late.promise);
			const props = mount();
			await screen.findByText('Listing open merge requests…');

			flowIs({});
			flushSync(() => (props.flowName = 'hotfix'));
			await waitFor(() => expect(screen.queryByText(/^Listing/)).not.toBeInTheDocument());

			late.resolve([{ mr: anOpenMr({ iid: 20 }), mine: false, runs: 0 }] as never);
			await settled();

			expect(props.mr).toBe(0);
		});
	});

	describe('skillsets', () => {
		it('asks by default', async () => {
			mount();
			expect(await screen.findByText('Skillsets')).toBeInTheDocument();
		});

		it('can leave them out', async () => {
			mount({ askSkillsets: false });
			await advanced();

			expect(screen.queryByText('Skillsets')).not.toBeInTheDocument();
			expect(api.skillsets.list).not.toHaveBeenCalled();
		});

		it('binds the sets picked', async () => {
			vi.mocked(api.skillsets.list).mockResolvedValue([
				{ id: 's1', name: 'Rust', description: '', headSeq: 1, skillIds: [] } as never
			]);
			const props = mount();

			await userEvent.click(await screen.findByRole('checkbox', { name: /Rust/ }));

			expect(props.skillsets).toEqual([{ id: 's1' }]);
		});
	});

	describe('harnesses and models', () => {
		const model = aParam({
			name: 'model',
			known: true,
			role: 'model',
			default: '',
			required: true,
			harness: 'claude'
		});
		const models = (...ids: string[]) => ids.map((id) => ({ id, name: id })) as unknown as Model[];
		const suggestions = () =>
			[...document.querySelectorAll('datalist option')].map((o) => o.getAttribute('value'));

		beforeEach(() => {
			flowIs({ parameters: [model] });
			vi.mocked(api.projects.get).mockResolvedValue(aProject({ harnessIds: ['h1', 'h3'] }));
			vi.mocked(api.integrations.list).mockResolvedValue([
				harness('h1', 'claude'),
				harness('h2', 'codex'),
				{ ...harness('h3', 'claude'), port: 'repository' } as Integration
			]);
		});

		it('suggests what the harness of the model parameter lists', async () => {
			vi.mocked(api.integrations.models).mockResolvedValue(models('acme/m1', 'acme/m2'));
			mount();

			await vi.waitFor(() => expect(suggestions()).toEqual(['acme/m1', 'acme/m2']));
			expect(api.integrations.models).toHaveBeenCalledExactlyOnceWith('h1');
		});

		it('leaves the field free when the harness cannot list', async () => {
			vi.mocked(api.integrations.models).mockRejectedValue(new Error('nope'));
			mount();
			const field = await screen.findByLabelText(/^model/);

			await vi.waitFor(() => expect(api.integrations.models).toHaveBeenCalled());
			await userEvent.type(field, 'acme/m3');

			expect(suggestions()).toEqual([]);
			expect(field).toHaveValue('acme/m3');
		});

		it('asks each harness once, however the form changes', async () => {
			vi.mocked(api.integrations.models).mockResolvedValue(models('acme/m1'));
			flowIs({ parameters: [model, aParam({ name: 'depth' })] });
			mount();

			await userEvent.type(await screen.findByLabelText(/^depth/), 'deep');

			expect(api.integrations.models).toHaveBeenCalledOnce();
		});

		it('asks nothing for a harness the workshop does not have', async () => {
			flowIs({ parameters: [{ ...model, harness: 'codex' }] });
			mount();
			await advanced();

			expect(api.integrations.models).not.toHaveBeenCalled();
		});

		it('offers only this workshop’s harnesses for a harness parameter', async () => {
			flowIs({
				parameters: [aParam({ name: 'harness', known: true, role: 'harness', default: '' })]
			});
			mount();

			await userEvent.click(await screen.findByRole('combobox', { name: /^harness/ }));

			const list = screen.getByRole('listbox');
			expect(within(list).getByRole('option', { name: 'claude · h1' })).toBeInTheDocument();
			expect(within(list).queryByRole('option', { name: /codex/ })).not.toBeInTheDocument();
			expect(within(list).queryByRole('option', { name: /h3/ })).not.toBeInTheDocument();
		});

		it('still asks for a model when the project cannot be read', async () => {
			vi.mocked(api.projects.get).mockRejectedValue(new Error('nope'));
			mount();

			expect(await screen.findByLabelText(/^model/)).toBeInTheDocument();
			expect(api.integrations.models).not.toHaveBeenCalled();
		});
	});
});
