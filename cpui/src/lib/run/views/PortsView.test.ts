import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { aViewerProject } from '$test/fixtures';
import type { ExposedPort, ObservedPort } from '$lib/domain';
import { CODE_SERVER_PORT } from '$lib/run/derive';
import { toasts } from '$lib/ui/toast.svelte';
import { aLiveRun, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import PortsView from './PortsView.svelte';

vi.mock('$lib/api', () => ({ api: { runs: { exposePort: vi.fn(), ports: vi.fn() } } }));

const RUN = '01TESTRUN0000000000000ABCD';
const URL = 'https://4000.preview.test/';
const openBtn = (port: number) =>
	screen.getByRole('button', { name: `Open :${port} in the browser` });
const openLink = (port: number) =>
	screen.getByRole('link', { name: `Open :${port} in the browser` });
const forwardBtn = (port: number) =>
	screen.getByRole('button', { name: `forward: copy apothecary forward env-1 ${port}` });
/** A port's row, as its cells read. */
const row = (port: number) =>
	within(screen.getByText(`:${port}`).closest('tr')!)
		.getAllByRole('cell')
		.map((c) => c.textContent?.trim());
const input = () => screen.getByRole('textbox', { name: 'Port' });
const openTyped = () => screen.getByRole('button', { name: 'Open ↵' });
/** A live run whose feed goes on answering what it already shows. */
function live(over: { observed?: ObservedPort[]; exposed?: ExposedPort[]; links?: boolean } = {}) {
	const links = over.links ?? true;
	vi.mocked(api.runs.ports).mockResolvedValue({
		observed: over.observed ?? [],
		exposed: over.exposed ?? [],
		links
	});
	const rs = aViewStore(aLiveRun(), over);
	rs.portLinks = links;
	return rs;
}

/** A tab window.open handed out: `location.href` is where it ended up. */
function blankTab() {
	const tab = { close: vi.fn(), location: { href: '' } };
	vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window);
	return tab;
}

describe('PortsView', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		toasts.list = [];
		vi.mocked(api.runs.exposePort).mockResolvedValue({ port: 4000, url: URL });
	});

	describe('the feed', () => {
		it('holds the ports feed while on screen, and lets go on hide', async () => {
			const rs = live();
			const release = vi.fn();
			const hold = vi.spyOn(rs, 'usePorts').mockReturnValue(release);

			const { rerender } = render(PortsView, { rs, visible: true });
			expect(hold).toHaveBeenCalledOnce();

			await rerender({ visible: false });
			expect(release).toHaveBeenCalledOnce();
		});

		it('does not hold the feed while hidden', () => {
			const rs = live();
			const hold = vi.spyOn(rs, 'usePorts');
			render(PortsView, { rs, visible: false });
			expect(hold).not.toHaveBeenCalled();
		});
	});

	describe('the table', () => {
		it('says when nothing is listening, without a table', () => {
			render(PortsView, { rs: live(), visible: true });
			expect(screen.getByText(/^Nothing listening yet/)).toBeInTheDocument();
			expect(screen.queryByRole('table')).not.toBeInTheDocument();
		});

		it('lists every port once, by number, with who listens and whether it is exposed', () => {
			render(PortsView, {
				rs: live({
					observed: [
						{ port: 5432, service: 'db', process: 'postgres' },
						{ port: 3000, process: 'node' },
						{ port: 9000 }
					],
					exposed: [{ port: 4000, url: URL, service: 'web' }]
				}),
				visible: true
			});
			expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toEqual([
				'Service',
				'Port',
				'Status',
				'Your machine',
				'Browser'
			]);
			const ports = screen.getAllByRole('row').slice(1);
			expect(ports.map((r) => within(r).getAllByRole('cell')[1].textContent)).toEqual([
				':3000',
				':4000',
				':5432',
				':9000'
			]);
			expect(row(5432).slice(0, 3)).toEqual(['db', ':5432', 'listening']);
			expect(row(3000).slice(0, 3)).toEqual(['node', ':3000', 'listening']);
			expect(row(9000).slice(0, 3)).toEqual(['—', ':9000', 'listening']);
			expect(row(4000).slice(0, 3)).toEqual(['web', ':4000', 'exposed']);
		});

		it('names an exposed port by its process when it has no service', () => {
			render(PortsView, {
				rs: live({ exposed: [{ port: 4000, url: URL, process: 'vite' }] }),
				visible: true
			});
			expect(row(4000)[0]).toBe('vite');
		});

		it('leaves out the editor’s port, which has its own button', () => {
			render(PortsView, {
				rs: live({ exposed: [{ port: CODE_SERVER_PORT, url: URL }] }),
				visible: true
			});
			expect(screen.queryByRole('table')).not.toBeInTheDocument();
			expect(screen.queryByRole('link')).not.toBeInTheDocument();
		});

		it('copies each port’s forward command from a button that says forward', async () => {
			const user = userEvent.setup();
			const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
			render(PortsView, {
				rs: live({
					observed: [{ port: 5432, service: 'db' }],
					exposed: [{ port: 4000, url: URL }]
				}),
				visible: true
			});
			expect(forwardBtn(4000)).toHaveTextContent('forward');

			await user.click(forwardBtn(5432));

			expect(write).toHaveBeenCalledExactlyOnceWith('apothecary forward env-1 5432');
			expect(api.runs.exposePort).not.toHaveBeenCalled();
		});

		it('says forward reaches any port, and open only web apps', () => {
			render(PortsView, { rs: live({ observed: [{ port: 5432 }] }), visible: true });
			expect(screen.getByText(/^forward copies the command/)).toHaveTextContent(
				'a database client'
			);
			expect(screen.getByText(/^open is for web apps only/)).toHaveTextContent(
				'a database or another TCP service does not answer there'
			);
		});

		it('links an exposed port in a new tab', () => {
			render(PortsView, { rs: live({ exposed: [{ port: 4000, url: URL }] }), visible: true });
			expect(openLink(4000)).toHaveAttribute('href', URL);
			expect(openLink(4000)).toHaveAttribute('target', '_blank');
		});
	});

	describe('without browser links', () => {
		it('lists and forwards ports, with no browser column, form or web-only hint', () => {
			render(PortsView, {
				rs: live({ links: false, observed: [{ port: 5432, service: 'db' }, { port: 3000 }] }),
				visible: true
			});
			expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toEqual([
				'Service',
				'Port',
				'Status',
				'Your machine'
			]);
			expect(forwardBtn(5432)).toBeInTheDocument();
			expect(screen.queryByRole('button', { name: /in the browser/ })).not.toBeInTheDocument();
			expect(screen.queryByText('Open another port in the browser')).not.toBeInTheDocument();
			expect(screen.queryByRole('textbox', { name: 'Port' })).not.toBeInTheDocument();
			expect(screen.queryByText(/^open is for web apps only/)).not.toBeInTheDocument();
			expect(screen.getByText(/^forward copies the command/)).toBeInTheDocument();
		});
	});

	describe('opening a listening port', () => {
		it('exposes on a click and sends the waiting tab there', async () => {
			const tab = blankTab();
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });

			await userEvent.click(openBtn(4000));

			expect(window.open).toHaveBeenCalledExactlyOnceWith('', '_blank');
			expect(api.runs.exposePort).toHaveBeenCalledExactlyOnceWith(RUN, 4000);
			expect(tab.location.href).toBe(URL);
		});

		it('turns the row into an exposed one with its link', async () => {
			blankTab();
			render(PortsView, {
				rs: live({ observed: [{ port: 4000, service: 'web' }] }),
				visible: true
			});
			vi.mocked(api.runs.ports).mockResolvedValue({
				exposed: [{ port: 4000, url: URL, service: 'web' }],
				observed: [],
				links: true
			});

			await userEvent.click(openBtn(4000));

			expect(
				await screen.findByRole('link', { name: 'Open :4000 in the browser' })
			).toHaveAttribute('href', URL);
			expect(row(4000).slice(0, 3)).toEqual(['web', ':4000', 'exposed']);
			expect(
				screen.queryByRole('button', { name: /:4000 in the browser/ })
			).not.toBeInTheDocument();
		});

		it('opens the tab inside the click, before the engine answers', async () => {
			const tab = blankTab();
			const exposing = deferred<{ port: number; url: string }>();
			vi.mocked(api.runs.exposePort).mockReturnValue(exposing.promise);
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });

			await userEvent.click(openBtn(4000));

			expect(window.open).toHaveBeenCalledOnce();
			expect(tab.location.href).toBe('');
			exposing.resolve({ port: 4000, url: URL });
		});

		it('opens a tab afterwards when the popup blocker ate the first', async () => {
			vi.spyOn(window, 'open').mockReturnValue(null);
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });

			await userEvent.click(openBtn(4000));

			await vi.waitFor(() => expect(window.open).toHaveBeenLastCalledWith(URL, '_blank'));
		});

		it('confirms with a toast that can open it again', async () => {
			const tab = blankTab();
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });
			await userEvent.click(openBtn(4000));

			await vi.waitFor(() => expect(toasts.list).toHaveLength(1));
			expect(toasts.list[0].action?.label).toBe('Open');

			toasts.list[0].action!.run();
			expect(window.open).toHaveBeenLastCalledWith(URL, '_blank');
			expect(tab.close).not.toHaveBeenCalled();
		});

		// t() groups number params by locale: not ":4,000".
		it('writes the port in the toast as a port', async () => {
			blankTab();
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });
			await userEvent.click(openBtn(4000));

			await vi.waitFor(() => expect(toasts.list).toHaveLength(1));
			expect(toasts.list[0].text).toBe('Port :4000 exposed');
		});

		it('closes the blank tab and says why when refused', async () => {
			const tab = blankTab();
			vi.mocked(api.runs.exposePort).mockRejectedValue(new Error('relay is full'));
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });

			await userEvent.click(openBtn(4000));

			expect(await screen.findByText('relay is full')).toBeInTheDocument();
			expect(tab.close).toHaveBeenCalledOnce();
			expect(toasts.list).toHaveLength(0);
		});

		it('holds every port while one is being exposed', async () => {
			blankTab();
			const exposing = deferred<{ port: number; url: string }>();
			vi.mocked(api.runs.exposePort).mockReturnValue(exposing.promise);
			render(PortsView, {
				rs: live({ observed: [{ port: 4000 }, { port: 5000 }] }),
				visible: true
			});

			await userEvent.click(openBtn(4000));

			expect(openBtn(4000)).toBeDisabled();
			expect(openBtn(4000)).toHaveAttribute('aria-busy', 'true');
			expect(openBtn(5000)).toBeDisabled();
			expect(openBtn(5000)).toHaveAttribute('aria-busy', 'false');
			exposing.resolve({ port: 4000, url: URL });
			await vi.waitFor(() => expect(openBtn(5000)).toBeEnabled());
		});
	});

	describe('opening another port', () => {
		it('opens what was typed on Enter and empties the box', async () => {
			blankTab();
			render(PortsView, { rs: live(), visible: true });

			await userEvent.type(input(), '4000{Enter}');

			expect(api.runs.exposePort).toHaveBeenCalledExactlyOnceWith(RUN, 4000);
			await vi.waitFor(() => expect(input()).toHaveValue(''));
		});

		it('opens from the button', async () => {
			blankTab();
			render(PortsView, { rs: live(), visible: true });
			await userEvent.type(input(), '65535');
			await userEvent.click(openTyped());
			expect(api.runs.exposePort).toHaveBeenCalledExactlyOnceWith(RUN, 65535);
		});

		it('offers nothing until a port is typed', () => {
			render(PortsView, { rs: live(), visible: true });
			expect(openTyped()).toBeDisabled();
		});

		it.each(['0', '-1', '65536', 'http'])('refuses %s without opening a tab', async (typed) => {
			const open = vi.spyOn(window, 'open');
			render(PortsView, { rs: live(), visible: true });

			await userEvent.type(input(), `${typed}{Enter}`);

			expect(screen.getByText('A port is 1–65535.')).toBeInTheDocument();
			expect(api.runs.exposePort).not.toHaveBeenCalled();
			expect(open).not.toHaveBeenCalled();
		});

		it.each(['8080x', '1e3', '80.5'])('refuses %s, which is no port', async (typed) => {
			blankTab();
			render(PortsView, { rs: live(), visible: true });

			await userEvent.type(input(), `${typed}{Enter}`);

			expect(api.runs.exposePort).not.toHaveBeenCalled();
		});

		it('keeps the port typed when the engine refuses it', async () => {
			blankTab();
			vi.mocked(api.runs.exposePort).mockRejectedValue(new Error('relay is full'));
			render(PortsView, { rs: live(), visible: true });

			await userEvent.type(input(), '4000{Enter}');

			expect(await screen.findByText('relay is full')).toBeInTheDocument();
			expect(input()).toHaveValue('4000');
		});

		it('clears the last failure on the next attempt', async () => {
			blankTab();
			render(PortsView, { rs: live(), visible: true });
			await userEvent.type(input(), '70000{Enter}');
			await screen.findByText('A port is 1–65535.');

			await userEvent.clear(input());
			await userEvent.type(input(), '4000{Enter}');

			await vi.waitFor(() =>
				expect(screen.queryByText('A port is 1–65535.')).not.toBeInTheDocument()
			);
		});

		it('shows the work on its button, not on a listed port', async () => {
			blankTab();
			const exposing = deferred<{ port: number; url: string }>();
			vi.mocked(api.runs.exposePort).mockReturnValue(exposing.promise);
			render(PortsView, { rs: live({ observed: [{ port: 5000 }] }), visible: true });

			await userEvent.type(input(), '4000{Enter}');

			expect(openTyped()).toHaveAttribute('aria-busy', 'true');
			expect(openTyped()).toBeDisabled();
			expect(openBtn(5000)).toHaveAttribute('aria-busy', 'false');
			exposing.resolve({ port: 4000, url: URL });
			await vi.waitFor(() => expect(input()).toHaveValue(''));
		});
	});

	describe('every port at once', () => {
		it('offers both forward commands, naming the workspace, and says how to see them', () => {
			render(PortsView, { rs: live({ observed: [{ port: 3000 }] }), visible: true });
			expect(screen.getByText('Every port at once')).toBeInTheDocument();
			expect(screen.getByText('apothecary forward env-1')).toBeInTheDocument();
			expect(screen.getByRole('button', { name: 'Copy apothecary forward env-1' })).toBeEnabled();
			expect(screen.getByText('apothecary forward env-1 --same')).toBeInTheDocument();
			expect(
				screen.getByRole('button', { name: 'Copy apothecary forward env-1 --same' })
			).toBeEnabled();
			expect(screen.getByText(/each on a free port of your machine/)).toHaveTextContent(
				'apothecary forward status'
			);
			expect(screen.getByText(/^The same port numbers on your machine/)).toBeInTheDocument();
		});

		it('says a forward takes the ports listening now, and --same is all or nothing', () => {
			render(PortsView, { rs: live({ observed: [{ port: 3000 }] }), visible: true });
			const all = screen.getByText(/each on a free port of your machine/);
			expect(all).toHaveTextContent('listens on now');
			expect(all).toHaveTextContent('for a port it opens later, run it again');
			expect(all).not.toHaveTextContent(/kept in step|open and close/);
			expect(screen.getByText(/^The same port numbers on your machine/)).toHaveTextContent(
				'nothing is forwarded, and the command prints itself again with a free port for each taken one'
			);
		});

		it('offers neither while nothing listens: the CLI would refuse it', () => {
			render(PortsView, { rs: live(), visible: true });
			expect(screen.getByText(/^Nothing listening yet/)).toBeInTheDocument();
			expect(screen.queryByText('Every port at once')).not.toBeInTheDocument();
			expect(screen.queryByText(/apothecary forward/)).not.toBeInTheDocument();
		});

		it('names the run until it has a workspace', () => {
			vi.mocked(api.runs.ports).mockResolvedValue({ observed: [], exposed: [], links: true });
			render(PortsView, {
				rs: aViewStore(aLiveRun({ envId: undefined }), { observed: [{ port: 3000 }] }),
				visible: true
			});
			expect(screen.getByText(`apothecary forward ${RUN}`)).toBeInTheDocument();
			expect(screen.getByText(`apothecary forward ${RUN} --same`)).toBeInTheDocument();
			expect(
				screen.getByRole('button', { name: `forward: copy apothecary forward ${RUN} 3000` })
			).toBeInTheDocument();
		});
	});

	describe('a finished run', () => {
		const feed = { observed: [{ port: 4000 }], exposed: [{ port: 5000, url: URL }], links: true };
		function over() {
			vi.mocked(api.runs.ports).mockResolvedValue(feed);
			return aViewStore(aLiveRun({ state: 'completed' }), feed);
		}

		it('forwards and exposes nothing more', () => {
			render(PortsView, { rs: over(), visible: true });
			expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toEqual([
				'Service',
				'Port',
				'Status',
				'Browser'
			]);
			expect(screen.queryByText(/apothecary forward/)).not.toBeInTheDocument();
			expect(screen.queryByRole('button', { name: /forward/ })).not.toBeInTheDocument();
			expect(screen.queryByRole('button', { name: /in the browser/ })).not.toBeInTheDocument();
			expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
			expect(screen.queryByText(/^forward copies the command/)).not.toBeInTheDocument();
		});

		it('keeps what is exposed reachable', () => {
			render(PortsView, { rs: over(), visible: true });
			expect(openLink(5000)).toHaveAttribute('href', URL);
		});
	});

	describe('for a viewer of the workshop', () => {
		afterEach(() => {
			fleet.projects = [];
		});
		it('forwards and exposes nothing, like a finished run', () => {
			fleet.projects = [aViewerProject({ id: 'p1' })];
			const feed = { observed: [{ port: 4000 }], exposed: [], links: true };
			vi.mocked(api.runs.ports).mockResolvedValue(feed);
			const rs = aViewStore(aLiveRun({ projectId: 'p1' }), feed);
			rs.portLinks = true;
			render(PortsView, { rs, visible: true });
			expect(screen.queryByRole('button', { name: /forward/ })).not.toBeInTheDocument();
			expect(screen.queryByRole('button', { name: /in the browser/ })).not.toBeInTheDocument();
			expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
			expect(screen.queryByText(/apothecary forward/)).not.toBeInTheDocument();
		});
	});
});
