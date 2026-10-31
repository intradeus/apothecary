import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { aProject } from '$test/fixtures';
import { aFlow, fill, pick } from '$test/fixtures/flow.svelte';
import { deferred } from '$test/helpers';
import CopyFlow from './CopyFlow.svelte';

vi.mock('$lib/api', () => ({ api: { flows: { copy: vi.fn(), copyTemplate: vi.fn() } } }));

const here = aProject({ id: 'p1', name: 'here' });
const there = aProject({ id: 'p2', name: 'there' });

const setup = (over: Partial<Parameters<typeof CopyFlow>[1]> = {}) => {
	const onclose = vi.fn();
	const oncopied = vi.fn();
	const props = { flow: aFlow(), projects: [here, there], sourceLabel: 'here', ...over };
	// `props:`, since a bare `target` would be read as where to mount.
	render(CopyFlow, { props: { ...props, onclose, oncopied } });
	return { onclose, oncopied };
};
const name = () => screen.getByLabelText('Name');
const into = () => screen.getByRole('combobox', { name: 'Into workshop' });
const submit = () => screen.getByRole('button', { name: 'Copy' });

describe('CopyFlow', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		vi.mocked(api.flows.copy).mockResolvedValue(aFlow({ projectId: 'p2', name: 'builder' }));
	});

	it('defaults to another workshop, under the same name', () => {
		setup();
		expect(screen.getByRole('dialog', { name: 'Copy builder' })).toBeInTheDocument();
		expect(into()).toHaveTextContent('there');
		expect(name()).toHaveValue('builder');
		expect(submit()).toBeEnabled();
	});

	it('takes the workshop it is given', () => {
		setup({ target: 'p1' });
		expect(into()).toHaveTextContent('here (same)');
	});

	it('copies inside the only workshop under a new name', () => {
		setup({ projects: [here] });
		expect(into()).toHaveTextContent('here (same)');
		expect(name()).toHaveValue('builder-copy');
		expect(submit()).toBeEnabled();
	});

	it('refuses the same name in the same workshop', async () => {
		setup();
		await pick('Into workshop', 'here (same)');
		expect(screen.getByText(/needs a different name/)).toBeInTheDocument();
		expect(submit()).toBeDisabled();
	});

	it('refuses a name that is no slug', async () => {
		setup();
		await fill(name(), 'Not Ok');
		expect(submit()).toBeDisabled();
	});

	it('refuses an empty name', async () => {
		setup();
		await fill(name(), '');
		expect(submit()).toBeDisabled();
	});

	it('copies from its project into the target', async () => {
		const { oncopied } = setup();
		await fill(name(), ' fork ');
		await userEvent.click(submit());
		expect(api.flows.copy).toHaveBeenCalledExactlyOnceWith('p1', 'builder', 'p2', 'fork');
		expect(api.flows.copyTemplate).not.toHaveBeenCalled();
		expect(oncopied).toHaveBeenCalledExactlyOnceWith('p2', 'builder');
	});

	// A template belongs to no project: it has its own route.
	it('adopts a template from its own route', async () => {
		vi.mocked(api.flows.copyTemplate).mockResolvedValue(aFlow({ projectId: 'p2' }));
		const { oncopied } = setup({ flow: aFlow({ builtin: true, projectId: '' }) });
		expect(screen.getByRole('dialog', { name: 'Use builder' })).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Copy into workshop' }));
		expect(api.flows.copyTemplate).toHaveBeenCalledExactlyOnceWith('builder', 'p1', 'builder');
		expect(api.flows.copy).not.toHaveBeenCalled();
		expect(oncopied).toHaveBeenCalledOnce();
	});

	it('holds the button while it copies', async () => {
		const copy = deferred<ReturnType<typeof aFlow>>();
		vi.mocked(api.flows.copy).mockReturnValue(copy.promise);
		setup();
		await userEvent.click(submit());
		const busy = screen.getByRole('button', { name: 'Copying…' });
		expect(busy).toBeDisabled();
		expect(busy).toHaveAttribute('aria-busy', 'true');
		copy.resolve(aFlow());
	});

	it('shows a failure and lets it be tried again', async () => {
		vi.mocked(api.flows.copy).mockRejectedValue(new Error('no space'));
		const { oncopied } = setup();
		await userEvent.click(submit());
		expect(await screen.findByText('no space')).toBeInTheDocument();
		expect(oncopied).not.toHaveBeenCalled();
		expect(submit()).toBeEnabled();
	});

	it('closes on Cancel', async () => {
		const { onclose } = setup();
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(onclose).toHaveBeenCalledOnce();
	});
});
