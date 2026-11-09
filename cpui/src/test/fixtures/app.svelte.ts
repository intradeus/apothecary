// Fixtures for the shell, workshop, system, tickets and inbox tests.
import { screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { vi } from 'vitest';
import type { Flow, Integration, Project } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { aProject } from '../fixtures';

/** Props a test can read back after a `$bindable` writes them: a component
 *  mounted on a `$state` object writes through to it, as under a `bind:`. */
export const bindable = <T extends object>(props: T): T => {
	const state = $state(props);
	return state;
};

const AT = '2026-10-31T12:00:00Z';

export const anIntegration = (over: Partial<Integration> = {}): Integration =>
	({
		id: 'int-1',
		name: 'Gitlab',
		port: 'repository',
		kind: 'gitlab',
		config: {},
		secrets: {},
		createdAt: AT,
		updatedAt: AT,
		...over
	}) as Integration;

export const aFlow = (over: Partial<Flow> = {}): Flow =>
	({
		projectId: '01TESTPROJ000000000000ABCD',
		name: 'builder',
		yaml: '',
		hash: 'e7c90c12deadbeef',
		updatedAt: AT,
		builtin: false,
		shadowsBuiltin: false,
		parameters: [],
		...over
	}) as Flow;

/** A project as the setup form and the workshop pages read it. */
export const aWorkshop = (over: Partial<Project> = {}): Project =>
	aProject({
		workspace: {
			egressMode: 'allow',
			extraDomains: [],
			deniedDomains: [],
			blockedDomains: [],
			allowedCidrs: [],
			allowedPorts: [],
			codeServerExtensions: [],
			services: [],
			browserEnabled: false,
			docker: false
		},
		...over
	} as Partial<Project>);

/** Picks an option of a Select by the Select's accessible name. */
export async function choose(select: string | RegExp, option: string | RegExp) {
	await userEvent.click(screen.getByRole('combobox', { name: select }));
	await userEvent.click(await screen.findByRole('option', { name: option }));
}

/** Answers the confirm dialog a click just raised. */
export async function confirmed(ok: boolean) {
	await vi.waitFor(() => {
		if (!confirmState.current) throw new Error('no confirm dialog');
	});
	const asked = confirmState.current!;
	confirmState.settle(ok);
	return asked;
}

/** Sets `document.hidden`; returns the setter. Restored by the caller's `restore`. */
export function visibility() {
	let hidden = false;
	const spy = vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden);
	return {
		set(value: boolean) {
			hidden = value;
			document.dispatchEvent(new Event('visibilitychange'));
		},
		spy
	};
}
