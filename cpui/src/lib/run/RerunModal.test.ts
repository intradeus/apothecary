import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { Run } from '$lib/domain';
import { aProject, aRun, aRunStore } from '$test/fixtures';
import { aFlow, aParam, bound } from '$test/fixtures/run-components';
import { deferred } from '$test/helpers';
import RerunModal from './RerunModal.svelte';

vi.mock('$lib/api', () => ({
	api: {
		runs: { rerun: vi.fn() },
		files: { upload: vi.fn(), limits: vi.fn() },
		flows: { list: vi.fn(), get: vi.fn(), binding: vi.fn() },
		projects: { get: vi.fn(), mergeRequests: vi.fn() },
		integrations: { list: vi.fn(), models: vi.fn() },
		skillsets: { list: vi.fn(), versions: vi.fn() }
	}
}));

const skillset = (id: string, name: string) =>
	({ id, name, description: '', headSeq: 3, skillIds: [] }) as never;

function open(over: Partial<Run> = {}) {
	const onstarted = vi.fn();
	const rs = aRunStore(aRun({ projectId: 'p1', ...over }));
	const props = bound({ rs, open: true, onstarted });
	render(RerunModal, { props });
	return { rs, props, onstarted };
}
const startBtn = () => screen.getByRole('button', { name: 'Start new run' });
const direction = () => screen.getByRole('textbox', { name: 'Direction for the new attempt' });
const flowPicker = () => screen.findByRole('combobox', { name: 'Flow' });
const fileInput = () => document.querySelector<HTMLInputElement>('input[type="file"]')!;
const sent = () => vi.mocked(api.runs.rerun).mock.calls[0][1]!;
const gone = () => waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

describe('RerunModal', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.spyOn(fleet, 'refresh').mockResolvedValue();
		vi.mocked(api.runs.rerun).mockResolvedValue('new-run-id');
		vi.mocked(api.files.limits).mockResolvedValue(null as never);
		vi.mocked(api.flows.list).mockResolvedValue([
			aFlow({ name: 'builder' }),
			aFlow({ name: 'hotfix' })
		]);
		vi.mocked(api.flows.get).mockResolvedValue(
			aFlow({ parameters: [aParam({ name: 'depth', default: 'shallow' })] })
		);
		vi.mocked(api.projects.get).mockResolvedValue(aProject());
		vi.mocked(api.integrations.list).mockResolvedValue([]);
		vi.mocked(api.skillsets.list).mockResolvedValue([]);
		vi.mocked(api.skillsets.versions).mockResolvedValue([]);
	});

	it('names the run it starts from and what stays as it is', () => {
		open();

		expect(screen.getByRole('dialog', { name: 'New run from r_abcd' })).toBeInTheDocument();
		expect(screen.getByText('new workspace').tagName).toBe('B');
	});

	it('shows nothing while closed', () => {
		render(RerunModal, { rs: aRunStore(), open: false, onstarted: vi.fn() });
		expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
	});

	describe('what it sends', () => {
		it('sends no overrides when nothing was touched', async () => {
			const { rs } = open();

			await userEvent.click(startBtn());

			expect(api.runs.rerun).toHaveBeenCalledExactlyOnceWith(rs.runId, {
				flow: undefined,
				goal: undefined,
				attachmentIds: undefined,
				params: undefined,
				skillsets: []
			});
			expect(api.files.upload).not.toHaveBeenCalled();
		});

		it('trims the direction', async () => {
			open();

			await userEvent.type(direction(), '  use the middleware  ');
			await userEvent.click(startBtn());

			expect(sent().goal).toBe('use the middleware');
		});

		it('sends a blank direction as none', async () => {
			open();

			await userEvent.type(direction(), '   ');
			await userEvent.click(startBtn());

			expect(sent().goal).toBeUndefined();
		});

		it('sends the flow picked, and reads that flow’s parameters', async () => {
			open();
			await userEvent.click(await flowPicker());
			await userEvent.click(await screen.findByRole('option', { name: 'hotfix' }));

			await vi.waitFor(() => expect(api.flows.get).toHaveBeenLastCalledWith('p1', 'hotfix'));
			await userEvent.click(startBtn());

			expect(sent().flow).toBe('hotfix');
		});

		it('reads the run’s own flow until another is picked', async () => {
			open({ flowName: 'builder' });

			await vi.waitFor(() => expect(api.flows.get).toHaveBeenCalledWith('p1', 'builder'));
			expect(await flowPicker()).toHaveTextContent('Same as this run (builder)');
		});

		it('takes a typed flow for a run without a workshop', async () => {
			open({ projectId: undefined });
			const flow = screen.getByRole('textbox', { name: 'Flow' });
			expect(flow).toHaveAttribute('placeholder', 'builder');

			await userEvent.type(flow, ' hotfix ');
			await userEvent.click(startBtn());

			expect(sent().flow).toBe('hotfix');
			expect(api.flows.get).not.toHaveBeenCalled();
		});

		it('sends the parameters overridden', async () => {
			open();

			await userEvent.type(await screen.findByLabelText(/^depth/), 'deep');
			await userEvent.click(startBtn());

			expect(sent().params).toEqual({ depth: 'deep' });
		});

		it('shows what the run used underneath', async () => {
			open({ params: { depth: 'medium' } });
			expect(await screen.findByText('this run: medium')).toBeInTheDocument();
		});

		it('re-pins the run’s skillsets to their latest', async () => {
			vi.mocked(api.skillsets.list).mockResolvedValue([skillset('s1', 'Rust')]);
			open({ skillsets: [{ id: 's1', name: 'Rust', seq: 2, hash: 'abc' }] });

			await screen.findByRole('checkbox', { name: /Rust/ });
			await userEvent.click(startBtn());

			expect(sent().skillsets).toEqual([{ id: 's1' }]);
		});

		it('sends an empty list when every skillset is cleared', async () => {
			vi.mocked(api.skillsets.list).mockResolvedValue([skillset('s1', 'Rust')]);
			open({ skillsets: [{ id: 's1', name: 'Rust', seq: 2, hash: 'abc' }] });

			await userEvent.click(await screen.findByRole('checkbox', { name: /Rust/ }));
			await userEvent.click(startBtn());

			expect(sent().skillsets).toEqual([]);
		});
	});

	describe('attachments', () => {
		const file = new File(['notes'], 'notes.md', { type: 'text/markdown' });

		it('uploads them first and sends their ids', async () => {
			vi.mocked(api.files.upload).mockResolvedValue([{ id: 'a1' }] as never);
			open();

			await userEvent.upload(fileInput(), file);
			await userEvent.click(startBtn());

			expect(api.files.upload).toHaveBeenCalledExactlyOnceWith([file]);
			expect(sent().attachmentIds).toEqual(['a1']);
		});

		it('starts nothing when the upload fails', async () => {
			vi.mocked(api.files.upload).mockRejectedValue(new Error('too big'));
			const { props, onstarted } = open();

			await userEvent.upload(fileInput(), file);
			await userEvent.click(startBtn());

			expect(await screen.findByText('too big')).toBeInTheDocument();
			expect(api.runs.rerun).not.toHaveBeenCalled();
			expect(onstarted).not.toHaveBeenCalled();
			expect(props.open).toBe(true);
		});
	});

	describe('a successful start', () => {
		it('closes, refreshes the fleet and hands over the new run', async () => {
			const { props, onstarted } = open();

			await userEvent.click(startBtn());

			await vi.waitFor(() => expect(onstarted).toHaveBeenCalledExactlyOnceWith('new-run-id'));
			expect(props.open).toBe(false);
			expect(fleet.refresh).toHaveBeenCalledOnce();
			await gone();
		});

		it('leaves no direction or files for next time', async () => {
			vi.mocked(api.files.upload).mockResolvedValue([{ id: 'a1' }] as never);
			const { props, onstarted } = open();
			await userEvent.type(direction(), 'try again');
			await userEvent.upload(fileInput(), new File(['x'], 'x.md'));
			await userEvent.click(startBtn());
			await vi.waitFor(() => expect(onstarted).toHaveBeenCalled());
			await gone();

			props.open = true;
			await tick();

			expect(direction()).toHaveValue('');
			expect(screen.queryByText('x.md')).not.toBeInTheDocument();
		});
	});

	describe('while starting', () => {
		it('holds the button, so one click is one run', async () => {
			const starting = deferred<string>();
			vi.mocked(api.runs.rerun).mockReturnValue(starting.promise);
			const { onstarted } = open();

			await userEvent.click(startBtn());

			expect(startBtn()).toBeDisabled();
			expect(startBtn()).toHaveAttribute('aria-busy', 'true');
			await userEvent.click(startBtn());
			expect(api.runs.rerun).toHaveBeenCalledOnce();

			starting.resolve('new-run-id');
			await vi.waitFor(() => expect(onstarted).toHaveBeenCalledOnce());
		});
	});

	describe('a failed start', () => {
		beforeEach(() => {
			vi.mocked(api.runs.rerun).mockRejectedValueOnce(new Error('no capacity'));
		});

		it('stays open with the reason and the draft', async () => {
			const { props, onstarted } = open();
			await userEvent.type(direction(), 'try again');

			await userEvent.click(startBtn());

			expect(await screen.findByText('no capacity')).toBeInTheDocument();
			expect(props.open).toBe(true);
			expect(direction()).toHaveValue('try again');
			expect(onstarted).not.toHaveBeenCalled();
			expect(fleet.refresh).not.toHaveBeenCalled();
		});

		it('offers the button again and clears the reason on the next try', async () => {
			const { onstarted } = open();
			await userEvent.click(startBtn());
			await screen.findByText('no capacity');
			expect(startBtn()).toBeEnabled();

			await userEvent.click(startBtn());

			await vi.waitFor(() => expect(onstarted).toHaveBeenCalledWith('new-run-id'));
			expect(screen.queryByText('no capacity')).not.toBeInTheDocument();
		});
	});

	describe('Cancel', () => {
		it('closes without starting anything', async () => {
			const { props } = open();

			await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));

			expect(props.open).toBe(false);
			await gone();
			expect(api.runs.rerun).not.toHaveBeenCalled();
		});

		it('opens again with no overrides', async () => {
			const { props } = open();
			await userEvent.type(await screen.findByLabelText(/^depth/), 'deep');
			expect(screen.getByText('1 overridden')).toBeInTheDocument();

			await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
			await gone();
			props.open = true;
			await tick();

			await screen.findByLabelText(/^depth/);
			expect(screen.queryByText('1 overridden')).not.toBeInTheDocument();
			await userEvent.click(startBtn());
			expect(sent().params).toBeUndefined();
		});

		it('opens again with the run’s skillsets, undoing what was cleared', async () => {
			vi.mocked(api.skillsets.list).mockResolvedValue([skillset('s1', 'Rust')]);
			const { props } = open({ skillsets: [{ id: 's1', name: 'Rust', seq: 2, hash: 'abc' }] });
			await userEvent.click(await screen.findByRole('checkbox', { name: /Rust/ }));

			await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
			await gone();
			props.open = true;
			await tick();

			expect(await screen.findByRole('checkbox', { name: /Rust/ })).toBeChecked();
		});
	});

	// "Each opening starts with no overrides" resets params and skillsets, but
	// the flow override and the last failure stay behind for the next opening.
	it('forgets the flow picked when it opens again', async () => {
		const { props } = open();
		await userEvent.click(await flowPicker());
		await userEvent.click(await screen.findByRole('option', { name: 'hotfix' }));

		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		await gone();
		props.open = true;
		await tick();

		expect(await flowPicker()).toHaveTextContent('Same as this run (builder)');
	});

	it('forgets the last failure when it opens again', async () => {
		vi.mocked(api.runs.rerun).mockRejectedValueOnce(new Error('no capacity'));
		const { props } = open();
		await userEvent.click(startBtn());
		await screen.findByText('no capacity');

		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		await gone();
		props.open = true;
		await tick();

		expect(screen.queryByText('no capacity')).not.toBeInTheDocument();
	});
});
