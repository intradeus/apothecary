import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { ExposedPort, ObservedPort } from '$lib/domain';
import { CODE_SERVER_PORT } from '$lib/run/derive';
import { toasts } from '$lib/ui/toast.svelte';
import { aLiveRun, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import PortsView from './PortsView.svelte';

vi.mock('$lib/api', () => ({ api: { runs: { exposePort: vi.fn(), ports: vi.fn() } } }));

const RUN = '01TESTRUN0000000000000ABCD';
const URL = 'https://4000.preview.test/';
const portBtn = (port: number) => screen.getByRole('button', { name: new RegExp(`:${port}`) });
const input = () => screen.getByRole('textbox', { name: 'Port' });
const exposeBtn = () => screen.getByRole('button', { name: 'Expose ↵' });
/** A live run whose feed goes on answering what it already shows. */
function live(over: { observed?: ObservedPort[]; exposed?: ExposedPort[] } = {}) {
	vi.mocked(api.runs.ports).mockResolvedValue({
		observed: over.observed ?? [],
		exposed: over.exposed ?? []
	});
	return aViewStore(aLiveRun(), over);
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

	describe('detected ports', () => {
		it('says when nothing new is listening', () => {
			render(PortsView, { rs: live(), visible: true });
			expect(screen.getByText(/^Nothing new listening/)).toBeInTheDocument();
		});

		it('names the service a port belongs to, over its process', () => {
			render(PortsView, {
				rs: live({ observed: [{ port: 4000, service: 'db', process: 'beam.smp' }] }),
				visible: true
			});
			expect(portBtn(4000)).toHaveTextContent(':4000 db');
			expect(portBtn(4000).title).toBe('Detected listener in service db — expose and open');
		});

		it('names its process otherwise', () => {
			render(PortsView, {
				rs: live({ observed: [{ port: 4000, process: 'beam.smp' }] }),
				visible: true
			});
			expect(portBtn(4000)).toHaveTextContent(':4000 beam.smp');
			expect(portBtn(4000).title).toBe('Detected listener (beam.smp) — expose and open');
		});

		it('settles for the port alone', () => {
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });
			expect(portBtn(4000).title).toBe('Detected listener — expose and open');
		});

		it('exposes on a click and sends the waiting tab there', async () => {
			const tab = blankTab();
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });

			await userEvent.click(portBtn(4000));

			expect(window.open).toHaveBeenCalledExactlyOnceWith('', '_blank');
			expect(api.runs.exposePort).toHaveBeenCalledExactlyOnceWith(RUN, 4000);
			expect(tab.location.href).toBe(URL);
		});

		it('drops the port off the list once it is exposed', async () => {
			blankTab();
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });
			vi.mocked(api.runs.ports).mockResolvedValue({
				exposed: [{ port: 4000, url: URL }],
				observed: []
			});

			await userEvent.click(await screen.findByRole('button', { name: /:4000/ }));

			expect(await screen.findByRole('link', { name: /:4000/ })).toHaveAttribute('href', URL);
			expect(screen.queryByRole('button', { name: /:4000/ })).not.toBeInTheDocument();
		});

		it('opens the tab inside the click, before the engine answers', async () => {
			const tab = blankTab();
			const exposing = deferred<{ port: number; url: string }>();
			vi.mocked(api.runs.exposePort).mockReturnValue(exposing.promise);
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });

			await userEvent.click(portBtn(4000));

			expect(window.open).toHaveBeenCalledOnce();
			expect(tab.location.href).toBe('');
			exposing.resolve({ port: 4000, url: URL });
		});

		it('opens a tab afterwards when the popup blocker ate the first', async () => {
			vi.spyOn(window, 'open').mockReturnValue(null);
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });

			await userEvent.click(portBtn(4000));

			await vi.waitFor(() => expect(window.open).toHaveBeenLastCalledWith(URL, '_blank'));
		});

		it('confirms with a toast that can open it again', async () => {
			const tab = blankTab();
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });
			await userEvent.click(portBtn(4000));

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
			await userEvent.click(portBtn(4000));

			await vi.waitFor(() => expect(toasts.list).toHaveLength(1));
			expect(toasts.list[0].text).toBe('Port :4000 exposed');
		});

		it('closes the blank tab and says why when refused', async () => {
			const tab = blankTab();
			vi.mocked(api.runs.exposePort).mockRejectedValue(new Error('relay is full'));
			render(PortsView, { rs: live({ observed: [{ port: 4000 }] }), visible: true });

			await userEvent.click(portBtn(4000));

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

			await userEvent.click(portBtn(4000));

			expect(portBtn(4000)).toBeDisabled();
			expect(portBtn(4000)).toHaveAttribute('aria-busy', 'true');
			expect(portBtn(5000)).toBeDisabled();
			expect(portBtn(5000)).toHaveAttribute('aria-busy', 'false');
			exposing.resolve({ port: 4000, url: URL });
			await vi.waitFor(() => expect(portBtn(5000)).toBeEnabled());
		});
	});

	describe('exposed ports', () => {
		it('hides the heading while nothing is exposed', () => {
			render(PortsView, { rs: live(), visible: true });
			expect(screen.queryByText('Exposed')).not.toBeInTheDocument();
		});

		it('links each exposed port in a new tab', () => {
			render(PortsView, { rs: live({ exposed: [{ port: 4000, url: URL }] }), visible: true });

			const link = screen.getByRole('link', { name: /:4000 open/ });
			expect(link).toHaveAttribute('href', URL);
			expect(link).toHaveAttribute('target', '_blank');
		});

		it('leaves out the editor’s port, which has its own button', () => {
			render(PortsView, {
				rs: live({ exposed: [{ port: CODE_SERVER_PORT, url: URL }] }),
				visible: true
			});
			expect(screen.queryByText('Exposed')).not.toBeInTheDocument();
			expect(screen.queryByRole('link')).not.toBeInTheDocument();
		});
	});

	describe('exposing by hand', () => {
		it('exposes what was typed on Enter and empties the box', async () => {
			blankTab();
			render(PortsView, { rs: live(), visible: true });

			await userEvent.type(input(), '4000{Enter}');

			expect(api.runs.exposePort).toHaveBeenCalledExactlyOnceWith(RUN, 4000);
			await vi.waitFor(() => expect(input()).toHaveValue(''));
		});

		it('exposes from the button', async () => {
			blankTab();
			render(PortsView, { rs: live(), visible: true });
			await userEvent.type(input(), '65535');
			await userEvent.click(exposeBtn());
			expect(api.runs.exposePort).toHaveBeenCalledExactlyOnceWith(RUN, 65535);
		});

		it('offers nothing until a port is typed', () => {
			render(PortsView, { rs: live(), visible: true });
			expect(exposeBtn()).toBeDisabled();
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

		it('shows the work on the button, not on a detected port', async () => {
			blankTab();
			const exposing = deferred<{ port: number; url: string }>();
			vi.mocked(api.runs.exposePort).mockReturnValue(exposing.promise);
			render(PortsView, { rs: live({ observed: [{ port: 5000 }] }), visible: true });

			await userEvent.type(input(), '4000{Enter}');

			expect(exposeBtn()).toHaveAttribute('aria-busy', 'true');
			expect(exposeBtn()).toBeDisabled();
			exposing.resolve({ port: 4000, url: URL });
			await vi.waitFor(() => expect(input()).toHaveValue(''));
		});
	});

	describe('a finished run', () => {
		const feed = { observed: [{ port: 4000 }], exposed: [{ port: 5000, url: URL }] };
		function over() {
			vi.mocked(api.runs.ports).mockResolvedValue(feed);
			return aViewStore(aLiveRun({ state: 'completed' }), feed);
		}

		it('can expose nothing more', () => {
			render(PortsView, { rs: over(), visible: true });
			expect(portBtn(4000)).toBeDisabled();
			expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
			expect(screen.queryByText('Expose a port')).not.toBeInTheDocument();
		});

		it('keeps what is exposed reachable', () => {
			render(PortsView, { rs: over(), visible: true });
			expect(screen.getByRole('link', { name: /:5000/ })).toBeInTheDocument();
		});
	});
});
