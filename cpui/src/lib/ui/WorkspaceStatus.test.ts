import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { Run } from '$lib/domain';
import WorkspaceStatus, { wsValue, type WsValue } from './WorkspaceStatus.svelte';

describe('wsValue', () => {
	it.each<[Run['envStatus'], WsValue]>([
		['rebuilding', 'rebuilding'],
		['destroying', 'destroying'],
		['error', 'error']
	])('lets the in-flight %s win over everything', (status, value) => {
		expect(wsValue('running', 'env-1', status)).toBe(value);
		expect(wsValue('provisioning', undefined, status)).toBe(value);
	});

	it('is provisioning on the run state alone, before any env id', () => {
		expect(wsValue('provisioning', undefined, undefined)).toBe('provisioning');
	});

	it('is provisioning on the engine status alone', () => {
		expect(wsValue('running', undefined, 'provisioning')).toBe('provisioning');
	});

	it('is none without an env, whatever docker last said', () => {
		expect(wsValue('running', undefined, undefined)).toBe('none');
		expect(wsValue('completed', undefined, 'running')).toBe('none');
	});

	it.each<[Run['envStatus'], WsValue]>([
		['running', 'live'],
		['starting', 'starting'],
		['stopped', 'stopped'],
		['absent', 'none'],
		['unknown', 'unknown']
	])('reads the engine status %s as %s', (status, value) => {
		expect(wsValue('running', 'env-1', status)).toBe(value);
	});

	it('assumes live before the first reconcile, so it never flashes "no workspace"', () => {
		expect(wsValue('running', 'env-1', undefined)).toBe('live');
	});
});

describe('WorkspaceStatus', () => {
	it('reads label, value and id', () => {
		render(WorkspaceStatus, { state: 'running', envId: 'ws_19b4', envStatus: 'running' });
		expect(screen.getByText('Workspace')).toBeInTheDocument();
		expect(screen.getByText('live')).toBeInTheDocument();
		expect(screen.getByText('ws_19b4')).toBeInTheDocument();
		expect(screen.getByTitle('Workspace: live · ws_19b4')).toBeInTheDocument();
	});

	it('drops the label when bare', () => {
		render(WorkspaceStatus, { state: 'running', envId: 'e', bare: true });
		expect(screen.queryByText('Workspace')).toBeNull();
		expect(screen.getByText('live')).toBeInTheDocument();
	});

	it('says "no workspace", with no dot and no id', () => {
		const { container } = render(WorkspaceStatus, {
			state: 'running',
			envId: 'ws_19b4',
			envStatus: 'absent'
		});
		expect(screen.getByText('no workspace')).toBeInTheDocument();
		expect(screen.queryByText('ws_19b4')).toBeNull();
		expect(container.querySelector('.ldot, .live-dot')).toBeNull();
	});

	it('tips without an env id when there is none', () => {
		render(WorkspaceStatus, { state: 'completed' });
		expect(screen.getByTitle('Workspace: no workspace')).toBeInTheDocument();
	});

	it('explains an unreachable provisioner on hover', () => {
		render(WorkspaceStatus, { state: 'running', envId: 'e', envStatus: 'unknown' });
		expect(screen.getByText('unknown')).toBeInTheDocument();
		expect(
			screen.getByTitle('Provisioner unreachable — check integration token')
		).toBeInTheDocument();
	});

	it.each<[Run['envStatus'], string, string, boolean]>([
		['running', 'hue-success', '.live-dot', false],
		['starting', 'hue-running', '.ldot.flat', true],
		['rebuilding', 'hue-running', '.ldot.flat', true],
		['destroying', 'hue-warning', '.ldot.flat', true],
		['error', 'hue-danger', '.ldot.flat', false],
		['stopped', 'hue-muted', '.ldot.hollow', false],
		['unknown', 'hue-warning', '.ldot.dashed', false]
	])('draws %s as DESIGN 3.2 says', (envStatus, hue, dot, pulsing) => {
		const { container } = render(WorkspaceStatus, { state: 'running', envId: 'e', envStatus });
		expect(container.firstElementChild).toHaveClass(hue);
		expect(container.querySelector(dot)?.classList.contains('pulse')).toBe(pulsing);
	});

	it('is only a dot with the value in a tooltip when compact', () => {
		const { container } = render(WorkspaceStatus, {
			state: 'running',
			envId: 'ws_19b4',
			envStatus: 'running',
			compact: true
		});
		expect(screen.getByTitle('Workspace: live · ws_19b4')).toContainElement(
			container.querySelector('.live-dot')
		);
		expect(screen.queryByText('live')).toBeNull();
		expect(screen.queryByText('ws_19b4')).toBeNull();
	});

	it('keeps a dashed ring for "no workspace" in the compact form', () => {
		const { container } = render(WorkspaceStatus, { state: 'completed', compact: true });
		expect(container.querySelector('.ldot.dashed')).not.toBeNull();
	});
});
