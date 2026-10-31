import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fleet } from '$lib/app/fleet.svelte';
import { aProject } from '$test/fixtures';
import { aDoc, aFlow, aStage, live } from '$test/fixtures/flow.svelte';
import type { FlowEditor } from './flowEditor.svelte';
import FlowHeader from './FlowHeader.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

const setup = (over: Record<string, unknown> = {}) => {
	const ed = live({
		projectId: 'p1',
		name: 'builder',
		flow: aFlow({ updatedAt: '2026-10-31T10:00:00Z' }),
		doc: aDoc({ description: 'Builds it', stages: [aStage('a'), aStage('b')] }),
		stageCount: 2,
		validation: null,
		validating: false,
		validateOpen: false,
		versions: [{}, {}, {}],
		showVersions: false,
		cloning: false,
		dirty: false,
		busy: false,
		tab: 'graph',
		switchTab: vi.fn(),
		validate: vi.fn(),
		save: vi.fn(),
		remove: vi.fn(),
		...over
	});
	render(FlowHeader, { ed: ed as unknown as FlowEditor });
	return ed;
};
const button = (name: string | RegExp) => screen.getByRole('button', { name });

describe('FlowHeader', () => {
	beforeEach(() => {
		vi.useFakeTimers({ toFake: ['Date'] });
		vi.setSystemTime('2026-10-31T12:00:00Z');
		fleet.projects = [aProject({ id: 'p1', name: 'dev', defaultFlow: 'other' })];
	});
	afterEach(() => {
		vi.useRealTimers();
		fleet.projects = [];
	});

	describe('what it says of the flow', () => {
		it('names it, its hash, its stages and when it was saved', () => {
			setup();
			expect(screen.getByRole('heading', { name: 'builder' })).toBeInTheDocument();
			expect(screen.getByText('@abcdef01')).toBeInTheDocument();
			expect(screen.getByText('Builds it')).toBeInTheDocument();
			expect(screen.getByText('2 stages')).toBeInTheDocument();
			expect(screen.getByText(/^saved 2h/)).toBeInTheDocument();
		});

		it('leads back to the workshop’s flows', () => {
			setup();
			expect(screen.getByRole('link', { name: 'dev' })).toHaveAttribute('href', '/w/p1');
			expect(screen.getByRole('link', { name: 'Flows' })).toHaveAttribute(
				'href',
				'/w/p1?tab=flows'
			);
		});

		it('waits for the workshop’s name', () => {
			fleet.projects = [];
			setup();
			expect(screen.getByRole('link', { name: '…' })).toBeInTheDocument();
		});

		it('names the flow before it has loaded, with no actions', () => {
			setup({ flow: null, doc: null });
			expect(screen.getByRole('heading', { name: 'builder' })).toBeInTheDocument();
			expect(screen.queryByRole('button', { name: 'Validate' })).not.toBeInTheDocument();
		});

		it('marks a template', () => {
			setup({ flow: aFlow({ builtin: true }) });
			expect(screen.getByText('built-in')).toBeInTheDocument();
		});

		it('marks a template’s own copy', () => {
			setup({ flow: aFlow({ shadowsBuiltin: true }) });
			expect(screen.getByText('from template')).toBeInTheDocument();
		});

		it('marks the workshop’s default', () => {
			fleet.projects = [aProject({ id: 'p1', defaultFlow: 'builder' })];
			setup();
			expect(screen.getByText('default')).toBeInTheDocument();
		});
	});

	describe('its tabs', () => {
		it('switches through the editor', async () => {
			const ed = setup();
			await userEvent.click(screen.getByRole('radio', { name: 'YAML' }));
			expect(ed.switchTab).toHaveBeenCalledExactlyOnceWith('yaml');
		});

		it('shows the one open', () => {
			setup({ tab: 'agents' });
			expect(screen.getByRole('radio', { name: 'Agents' })).toBeChecked();
		});
	});

	describe('saving', () => {
		it('holds Save until there is something to save', () => {
			setup();
			expect(screen.getByText('The formula')).toBeInTheDocument();
			expect(button('Save')).toBeDisabled();
		});

		it('saves changes, saying they are unsaved', async () => {
			const ed = setup({ dirty: true });
			expect(screen.getByText('Unsaved changes')).toBeInTheDocument();
			await userEvent.click(button('Save'));
			expect(ed.save).toHaveBeenCalledOnce();
		});

		it('holds Save while it saves', () => {
			setup({ dirty: true, busy: true });
			const save = button('Saving…');
			expect(save).toBeDisabled();
			expect(save).toHaveAttribute('aria-busy', 'true');
		});

		it('saves a template as a copy', () => {
			setup({ dirty: true, flow: aFlow({ builtin: true }) });
			expect(button('Save as copy')).toHaveAttribute(
				'title',
				'Saving creates this workshop’s editable copy'
			);
		});
	});

	describe('its other actions', () => {
		it('validates', async () => {
			const ed = setup();
			await userEvent.click(button('Validate'));
			expect(ed.validate).toHaveBeenCalledOnce();
		});

		it('shows Validate failing and counts warnings', () => {
			setup({ validation: { ok: false, title: 'Invalid', warnings: ['a', 'b'] } });
			expect(button(/Validate/)).toHaveClass('btn-danger');
			expect(button(/Validate/)).toHaveTextContent('2');
		});

		it('shows a validation in flight', () => {
			setup({ validating: true });
			expect(button('Validate')).toHaveAttribute('aria-busy', 'true');
		});

		it('toggles the versions, counting them', async () => {
			const ed = setup();
			const btn = button(/Versions/);
			expect(btn).toHaveTextContent('3');
			expect(btn).toHaveAttribute('aria-pressed', 'false');
			await userEvent.click(btn);
			expect(ed.showVersions).toBe(true);
			expect(btn).toHaveAttribute('aria-pressed', 'true');
		});

		it('opens the clone dialog', async () => {
			const ed = setup();
			await userEvent.click(button('Clone'));
			expect(ed.cloning).toBe(true);
		});

		it('holds Clone while it saves', () => {
			setup({ busy: true });
			expect(button('Clone')).toBeDisabled();
		});

		it('deletes a flow of the workshop’s own', async () => {
			const ed = setup();
			await userEvent.click(button('Delete…'));
			expect(ed.remove).toHaveBeenCalledOnce();
		});

		it('resets a template’s copy instead', async () => {
			const ed = setup({ flow: aFlow({ shadowsBuiltin: true }) });
			expect(screen.queryByRole('button', { name: 'Delete…' })).not.toBeInTheDocument();
			await userEvent.click(button('Reset to built-in…'));
			expect(ed.remove).toHaveBeenCalledOnce();
		});

		it('removes nothing from a template', () => {
			setup({ flow: aFlow({ builtin: true }) });
			expect(screen.queryByRole('button', { name: 'Delete…' })).not.toBeInTheDocument();
			expect(screen.queryByRole('button', { name: /Reset/ })).not.toBeInTheDocument();
		});
	});

	it('shows the validation in its popover once open', () => {
		setup({ validateOpen: true, validation: { ok: true, title: 'All good' } });
		expect(screen.getByText('All good')).toBeInTheDocument();
	});
});
