import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { RunSsh } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { bound, settled } from '$test/fixtures/run-components';
import { aLiveRun, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import { vscodeRemoteSettings } from '../derive';
import EnvMenu from './EnvMenu.svelte';

vi.mock('$lib/api', () => ({
	api: { runs: { ssh: vi.fn(), ports: vi.fn(), services: vi.fn() } }
}));

const door = (over: Partial<RunSsh> = {}): RunSsh =>
	({ enabled: true, keys: 1, command: 'ssh -p 2222 apo-x@door.test', ...over }) as RunSsh;

const writeText = vi.fn();

function setup(open = true, store = aViewStore(aLiveRun())) {
	const props = bound({ rs: store, anchor: null, open });
	render(EnvMenu, { props });
	return { rs: store, props };
}

async function ready(ssh: RunSsh, store = aViewStore(aLiveRun())) {
	vi.mocked(api.runs.ssh).mockResolvedValue(ssh);
	const view = setup(true, store);
	await settled();
	return view;
}

const row = (name: string | RegExp) => screen.getByRole('button', { name });

describe('EnvMenu', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		toasts.list = [];
		vi.mocked(api.runs.ports).mockResolvedValue({ exposed: [], observed: [] } as never);
		vi.mocked(api.runs.services).mockResolvedValue({ workspace: true, services: [] });
		Object.defineProperty(navigator, 'clipboard', {
			value: { writeText },
			configurable: true
		});
		writeText.mockResolvedValue(undefined);
	});

	afterEach(() => {
		vi.useRealTimers();
	});

	describe('while shut', () => {
		it('asks nothing and holds no feed', async () => {
			const rs = aViewStore(aLiveRun());
			const ports = vi.spyOn(rs, 'usePorts');
			setup(false, rs);
			await settled();
			expect(api.runs.ssh).not.toHaveBeenCalled();
			expect(ports).not.toHaveBeenCalled();
		});
	});

	describe('the feeds', () => {
		it('holds the ports and services while open, and lets go on closing', async () => {
			const rs = aViewStore(aLiveRun());
			const releasePorts = vi.fn();
			const releaseServices = vi.fn();
			vi.spyOn(rs, 'usePorts').mockReturnValue(releasePorts);
			vi.spyOn(rs, 'useServices').mockReturnValue(releaseServices);
			vi.mocked(api.runs.ssh).mockResolvedValue(door());

			const { props } = setup(true, rs);
			await settled();
			expect(rs.usePorts).toHaveBeenCalledOnce();
			expect(rs.useServices).toHaveBeenCalledOnce();
			expect(releasePorts).not.toHaveBeenCalled();

			props.open = false;
			await settled();
			expect(releasePorts).toHaveBeenCalledOnce();
			expect(releaseServices).toHaveBeenCalledOnce();
		});
	});

	describe('the SSH door', () => {
		it('says it is asking', async () => {
			vi.mocked(api.runs.ssh).mockReturnValue(deferred<RunSsh>().promise);
			setup();
			await settled();
			const asking = screen.getByText('Asking the SSH door…');
			expect(asking.closest('[aria-busy]')).toHaveAttribute('aria-busy', 'true');
		});

		it('says when it could not read the door', async () => {
			vi.mocked(api.runs.ssh).mockRejectedValue(new Error('down'));
			setup();
			await settled();
			const failed = screen.getByText('Could not read the SSH door');
			expect(failed.closest('[aria-busy]')).toHaveAttribute('aria-busy', 'false');
		});

		it('says why it is off', async () => {
			await ready(door({ enabled: false, reason: 'Not on this deployment' }));
			expect(screen.getByText('SSH is off')).toBeInTheDocument();
			expect(screen.getByText('Not on this deployment')).toBeInTheDocument();
		});

		it('sends someone with no key to register one', async () => {
			await ready(door({ keys: 0 }));
			expect(screen.getByRole('link', { name: /register an ssh key/i })).toHaveAttribute(
				'href',
				'/system/account'
			);
			expect(screen.queryByRole('button', { name: /^SSH/ })).not.toBeInTheDocument();
		});

		it('reads the door again at each opening: a key added elsewhere counts', async () => {
			vi.mocked(api.runs.ssh).mockResolvedValueOnce(door({ keys: 0 }));
			const { props } = setup();
			await settled();
			props.open = false;
			await settled();

			vi.mocked(api.runs.ssh).mockResolvedValueOnce(door());
			props.open = true;
			await settled();

			expect(api.runs.ssh).toHaveBeenCalledTimes(2);
			expect(row(/^SSH/)).toBeInTheDocument();
		});

		it('keeps the last answer up while a fresh one is on its way', async () => {
			vi.mocked(api.runs.ssh).mockResolvedValueOnce(door());
			const { props } = setup();
			await settled();
			props.open = false;
			await settled();

			vi.mocked(api.runs.ssh).mockReturnValueOnce(deferred<RunSsh>().promise);
			props.open = true;
			await settled();

			expect(row(/^SSH/)).toBeInTheDocument();
			expect(screen.queryByText('Asking the SSH door…')).not.toBeInTheDocument();
		});

		it('ignores an answer that lands after the menu closed', async () => {
			const first = deferred<RunSsh>();
			vi.mocked(api.runs.ssh).mockReturnValueOnce(first.promise);
			const { props } = setup();
			await settled();
			props.open = false;
			await settled();

			vi.mocked(api.runs.ssh).mockResolvedValueOnce(door({ keys: 0 }));
			props.open = true;
			await settled();
			first.resolve(door({ enabled: false, reason: 'Stale' }));
			await settled();

			expect(screen.queryByText('Stale')).not.toBeInTheDocument();
			expect(screen.getByText('Register an SSH key to connect')).toBeInTheDocument();
		});

		it("forgets another run's door", async () => {
			const rs = aViewStore(aLiveRun({ id: 'run-a' }));
			vi.mocked(api.runs.ssh).mockResolvedValueOnce(door());
			const { props } = setup(true, rs);
			await settled();
			props.open = false;
			await settled();

			rs.run = aLiveRun({ id: 'run-b' });
			vi.mocked(api.runs.ssh).mockReturnValueOnce(deferred<RunSsh>().promise);
			props.open = true;
			await settled();

			expect(api.runs.ssh).toHaveBeenLastCalledWith('run-b');
			expect(screen.getByText('Asking the SSH door…')).toBeInTheDocument();
		});
	});

	describe('getting in', () => {
		it('links VS Code, naming the folder it opens', async () => {
			await ready(door({ vscodeUrl: 'vscode://x', path: '/home/dev/repo' }));
			const link = screen.getByRole('link', { name: /open in vs code/i });
			expect(link).toHaveAttribute('href', 'vscode://x');
			expect(screen.getByText('/home/dev/repo')).toBeInTheDocument();
		});

		it('says how it gets there when it has no folder', async () => {
			await ready(door({ vscodeUrl: 'vscode://x' }));
			expect(screen.getByText('Remote-SSH, through the engine’s door')).toBeInTheDocument();
		});

		it('offers no VS Code rows without a link', async () => {
			await ready(door());
			expect(screen.queryByRole('link', { name: /vs code/i })).not.toBeInTheDocument();
			expect(screen.queryByText('Your extensions, over there too')).not.toBeInTheDocument();
		});

		it('offers no SSH row without a command', async () => {
			await ready(door({ command: undefined }));
			expect(screen.queryByRole('button', { name: /^SSH/ })).not.toBeInTheDocument();
		});

		it('shows the fingerprint to check on first connect', async () => {
			await ready(door({ fingerprint: 'SHA256:abc' }));
			expect(screen.getByText('door fingerprint SHA256:abc')).toBeInTheDocument();
		});
	});

	describe('copying', () => {
		it('copies the ssh command and confirms it', async () => {
			await ready(door());
			await userEvent.click(row(/^SSH/));
			expect(writeText).toHaveBeenCalledWith('ssh -p 2222 apo-x@door.test');
			expect(toasts.list.map((t) => t.text)).toEqual(['Copied the SSH command']);
		});

		it("copies the extension settings, pre-filled with the workspace's list", async () => {
			await ready(door({ vscodeUrl: 'vscode://x', extensions: ['a.b'] } as Partial<RunSsh>));
			await userEvent.click(row(/your extensions/i));
			expect(writeText).toHaveBeenCalledWith(vscodeRemoteSettings(['a.b']));
			expect(toasts.list.map((t) => t.text)).toEqual([
				'Copied the settings.json lines for the desktop'
			]);
		});

		it('marks the row copied, then goes back', async () => {
			await ready(door());
			vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
			await fireEvent.click(row(/^SSH/));
			await settled();
			expect(row(/^SSH/)).toHaveTextContent('copied');

			vi.advanceTimersByTime(1500);
			await tick();
			expect(row(/^SSH/)).toHaveTextContent(/copy$/);
		});

		it('marks only the row that was copied', async () => {
			await ready(door({ hostKey: 'ssh-ed25519 AAAA', host: 'door.test' }));
			await userEvent.click(row(/^SSH/));
			await waitFor(() => expect(row(/^SSH/)).toHaveTextContent('copied'));
			expect(row(/known_hosts/)).toHaveTextContent(/copy$/);
		});

		it('keeps the failure for the header when the browser refuses', async () => {
			writeText.mockRejectedValue(new Error('denied'));
			const { rs } = await ready(door());
			await userEvent.click(row(/^SSH/));
			await waitFor(() =>
				expect(rs.error).toBe('Could not copy — the browser refused clipboard access.')
			);
			expect(toasts.list).toEqual([]);
			expect(row(/^SSH/)).toHaveTextContent(/copy$/);
		});

		describe('the known_hosts line', () => {
			const hostKey = 'ssh-ed25519 AAAA';

			it.each([
				['no port', { host: 'door.test' }, 'door.test ssh-ed25519 AAAA'],
				['the default port', { host: 'door.test', port: 22 }, 'door.test ssh-ed25519 AAAA'],
				['another port', { host: 'door.test', port: 2222 }, '[door.test]:2222 ssh-ed25519 AAAA']
			])('is pinned for %s', async (_, over, line) => {
				await ready(door({ hostKey, ...over }));
				await userEvent.click(row(/known_hosts/));
				expect(writeText).toHaveBeenCalledWith(line);
				expect(toasts.list.map((t) => t.text)).toEqual(['Copied the known_hosts line']);
			});

			it.each([
				['a host', { hostKey }],
				['a host key', { host: 'door.test' }]
			])('is not offered without %s', async (_, over) => {
				await ready(door(over));
				expect(screen.queryByRole('button', { name: /known_hosts/ })).not.toBeInTheDocument();
			});
		});
	});

	describe('what the workspace serves', () => {
		const serving = (...exposed: { port: number; url: string }[]) =>
			vi.mocked(api.runs.ports).mockResolvedValue({ exposed, observed: [] } as never);

		it('links each app, in a new tab, and counts them', async () => {
			serving({ port: 3000, url: 'http://app.test' });
			await ready(door());
			const app = screen.getByRole('link', { name: /app :3000/ });
			expect(app).toHaveAttribute('href', 'http://app.test');
			expect(app).toHaveAttribute('target', '_blank');
			expect(screen.getByText('1 link')).toBeInTheDocument();
		});

		it('says links, plural, for two', async () => {
			serving({ port: 3000, url: 'http://a.test' }, { port: 4000, url: 'http://b.test' });
			await ready(door());
			expect(screen.getByText('2 links')).toBeInTheDocument();
		});

		it('has no count with no links', async () => {
			await ready(door());
			expect(screen.queryByText(/\d links?/)).not.toBeInTheDocument();
		});

		it('still shows them when the door cannot be read', async () => {
			serving({ port: 3000, url: 'http://app.test' });
			vi.mocked(api.runs.ssh).mockRejectedValue(new Error('down'));
			setup();
			await settled();
			expect(screen.getByRole('link', { name: /app :3000/ })).toBeInTheDocument();
		});
	});
});
