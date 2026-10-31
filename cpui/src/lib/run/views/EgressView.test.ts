import { fireEvent, render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { to } from '$lib/core/urls';
import type { EgressAuditRow, EgressPolicy } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aProject, aRun } from '$test/fixtures';
import { NOW, aLiveRun, aPolicy, anAuditRow, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import EgressView from './EgressView.svelte';

vi.mock('$lib/api', () => ({
	api: {
		egress: { runAudit: vi.fn(), policy: vi.fn(), setRule: vi.fn(), denials: vi.fn() }
	}
}));

const RUN = '01TESTRUN0000000000000ABCD';
const group = (host: string) => screen.getByRole('radiogroup', { name: `Rule for ${host}` });
const ruleRadio = (host: string, name: string) => within(group(host)).getByRole('radio', { name });
/** The rule a host's toggle stands on. */
const rule = (host: string) =>
	within(group(host))
		.getAllByRole('radio')
		.find((r) => r.getAttribute('aria-checked') === 'true')?.textContent;
const filter = (name: RegExp) => screen.getByRole('radio', { name });
const hosts = () => screen.queryAllByTitle(/\(10\.0\.0\.\d\)$/).map((e) => e.textContent?.trim());

async function open(
	rows: EgressAuditRow[] = [anAuditRow()],
	policy: EgressPolicy | null = aPolicy({ domains: ['api.test'] }),
	rs = aViewStore(aLiveRun())
) {
	vi.mocked(api.egress.runAudit).mockResolvedValue(rows);
	vi.mocked(api.egress.policy).mockResolvedValue(policy);
	vi.mocked(api.egress.denials).mockResolvedValue([]);
	const view = render(EgressView, { rs, visible: true });
	await vi.waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument());
	await tick();
	return { ...view, rs };
}

describe('EgressView', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		toasts.list = [];
		fleet.projects = [];
		vi.setSystemTime(NOW);
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	describe('states', () => {
		it('says it is connecting until the audit lands', async () => {
			const audit = deferred<EgressAuditRow[]>();
			vi.mocked(api.egress.runAudit).mockReturnValue(audit.promise);
			vi.mocked(api.egress.policy).mockResolvedValue(aPolicy());
			render(EgressView, { rs: aViewStore(aLiveRun()), visible: true });

			expect(screen.getByRole('status')).toHaveTextContent('Connecting…');
			audit.resolve([]);
			expect(await screen.findByText(/^No egress recorded yet/)).toBeInTheDocument();
		});

		it('offers a retry when the audit cannot be read, and recovers', async () => {
			vi.mocked(api.egress.runAudit).mockRejectedValueOnce(new Error('store down'));
			vi.mocked(api.egress.policy).mockResolvedValue(aPolicy());
			render(EgressView, { rs: aViewStore(aLiveRun()), visible: true });

			const alert = await screen.findByRole('alert');
			expect(alert).toHaveTextContent('The egress audit is unavailable.');
			expect(alert).toHaveTextContent('store down');
			expect(screen.queryByRole('status')).not.toBeInTheDocument();

			vi.mocked(api.egress.runAudit).mockResolvedValue([anAuditRow()]);
			await userEvent.click(within(alert).getByRole('button', { name: /Retry/ }));

			expect(await screen.findByText('api.test')).toBeInTheDocument();
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		// Nothing is connecting: a finished run does not poll, so this never ends.
		it('does not claim to connect once a failed read is dismissed', async () => {
			vi.mocked(api.egress.runAudit).mockRejectedValue(new Error('store down'));
			render(EgressView, { rs: aViewStore(aRun({ state: 'completed' })), visible: true });
			const alert = await screen.findByRole('alert');

			await userEvent.click(within(alert).getByRole('button', { name: 'Dismiss' }));

			expect(screen.queryByText('Connecting…')).not.toBeInTheDocument();
			expect(screen.getByText('The egress audit is unavailable.')).toBeInTheDocument();
		});

		it('keeps the rows when a later read fails', async () => {
			vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
			await open();
			vi.mocked(api.egress.runAudit).mockRejectedValue(new Error('blip'));

			await vi.advanceTimersByTimeAsync(5000);

			expect(screen.getByText('api.test')).toBeInTheDocument();
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('says nothing has gone out yet', async () => {
			await open([]);
			expect(screen.getByText(/^No egress recorded yet/)).toBeInTheDocument();
			expect(screen.queryByText('Nothing matches.')).not.toBeInTheDocument();
		});

		it('shows the history without toggles when there is no workspace', async () => {
			const rs = aViewStore(aRun({ state: 'completed' }));
			await open([anAuditRow()], null, rs);

			expect(api.egress.policy).not.toHaveBeenCalled();
			expect(screen.getByText('api.test')).toBeInTheDocument();
			expect(screen.queryByRole('radiogroup', { name: /^Rule for/ })).not.toBeInTheDocument();
			expect(screen.getByTitle('No live gateway — the workspace is gone')).toBeInTheDocument();
		});

		it('shows no toggles when the gateway answers it has no policy', async () => {
			await open([anAuditRow()], null);
			expect(screen.queryByRole('radiogroup', { name: /^Rule for/ })).not.toBeInTheDocument();
		});

		it('stays quiet when the policy cannot be read', async () => {
			vi.mocked(api.egress.runAudit).mockResolvedValue([anAuditRow()]);
			vi.mocked(api.egress.policy).mockRejectedValue(new Error('no gateway'));
			render(EgressView, { rs: aViewStore(aLiveRun()), visible: true });

			expect(await screen.findByText('api.test')).toBeInTheDocument();
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('takes the toggles away when the workspace goes', async () => {
			const { rs } = await open();
			expect(rule('api.test')).toBe('allow');

			rs.run = aRun({ state: 'completed' });
			await tick();

			expect(screen.queryByRole('radiogroup', { name: /^Rule for/ })).not.toBeInTheDocument();
		});
	});

	describe('the rule now', () => {
		it('allows a listed host in allowlist mode', async () => {
			await open();
			expect(rule('api.test')).toBe('allow');
		});

		it('holds a host nobody ruled on, with held as a third choice', async () => {
			await open([anAuditRow({ host: 'new.test' })], aPolicy());
			expect(rule('new.test')).toBe('held');
			expect(within(group('new.test')).getAllByRole('radio')).toHaveLength(3);
		});

		it('offers no way back to held once a host is ruled on', async () => {
			await open();
			expect(within(group('api.test')).queryByRole('radio', { name: 'held' })).toBeNull();
		});

		it('blocks a force-blocked host even when the list allows it', async () => {
			await open([anAuditRow()], aPolicy({ domains: ['api.test'], blockedDomains: ['api.test'] }));
			expect(rule('api.test')).toBe('block');
		});

		it('lets a force-block win in denylist mode too', async () => {
			await open(
				[anAuditRow({ host: 'x.test' })],
				aPolicy({ mode: 'deny', blockedDomains: ['x.test'] })
			);
			expect(rule('x.test')).toBe('block');
		});

		it('reads the list the other way round in denylist mode', async () => {
			await open(
				[anAuditRow({ host: 'bad.test' }), anAuditRow({ host: 'ok.test' })],
				aPolicy({ mode: 'deny', domains: ['bad.test'] })
			);
			expect(rule('bad.test')).toBe('block');
			expect(rule('ok.test')).toBe('allow');
		});

		it('matches a wildcard by suffix, never the bare apex', async () => {
			await open(
				[anAuditRow({ host: 'a.b.test' }), anAuditRow({ host: 'b.test' })],
				aPolicy({ domains: ['*.b.test'] })
			);
			expect(rule('a.b.test')).toBe('allow');
			expect(rule('b.test')).toBe('held');
		});

		it('ignores case', async () => {
			await open([anAuditRow({ host: 'API.Test' })], aPolicy({ domains: ['api.TEST'] }));
			expect(rule('API.Test')).toBe('allow');
		});
	});

	describe('changing a rule', () => {
		const settle = () => {
			vi.mocked(api.egress.setRule).mockResolvedValue(true);
		};

		it('allows a held host, reads the rule back and refreshes the banner', async () => {
			settle();
			await open([anAuditRow()], aPolicy());
			vi.mocked(api.egress.policy).mockResolvedValue(aPolicy({ domains: ['api.test'] }));

			await userEvent.click(ruleRadio('api.test', 'allow'));

			expect(api.egress.setRule).toHaveBeenCalledExactlyOnceWith(RUN, 'api.test', true);
			expect(api.egress.policy).toHaveBeenCalledTimes(2);
			expect(api.egress.denials).toHaveBeenCalledOnce();
			expect(rule('api.test')).toBe('allow');
		});

		it('offers no undo on a first review: held is no rule to go back to', async () => {
			settle();
			await open([anAuditRow()], aPolicy());
			await userEvent.click(ruleRadio('api.test', 'allow'));

			expect(toasts.list).toHaveLength(1);
			expect(toasts.list[0]).toMatchObject({ text: 'api.test allowed', tone: 'ok' });
			expect(toasts.list[0].action).toBeUndefined();
		});

		it('blocks a host with a warning and an undo', async () => {
			settle();
			await open();
			await userEvent.click(ruleRadio('api.test', 'block'));

			expect(api.egress.setRule).toHaveBeenCalledExactlyOnceWith(RUN, 'api.test', false);
			expect(toasts.list[0]).toMatchObject({ text: 'api.test blocked', tone: 'warn' });
			expect(toasts.list[0].action?.label).toBe('Undo');
		});

		it('puts the old rule back on undo, without offering another undo', async () => {
			settle();
			await open();
			await userEvent.click(ruleRadio('api.test', 'block'));

			toasts.list[0].action!.run();
			await vi.waitFor(() => expect(toasts.list).toHaveLength(2));

			expect(api.egress.setRule).toHaveBeenLastCalledWith(RUN, 'api.test', true);
			expect(toasts.list[1]).toMatchObject({ text: 'api.test allowed again', tone: 'ok' });
			expect(toasts.list[1].action).toBeUndefined();
		});

		it('says blocked again when undoing an allow', async () => {
			settle();
			await open([anAuditRow()], aPolicy({ blockedDomains: ['api.test'] }));
			await userEvent.click(ruleRadio('api.test', 'allow'));

			toasts.list[0].action!.run();
			await vi.waitFor(() => expect(toasts.list).toHaveLength(2));

			expect(api.egress.setRule).toHaveBeenLastCalledWith(RUN, 'api.test', false);
			expect(toasts.list[1].text).toBe('api.test blocked again');
		});

		it('flips the moment it is clicked', async () => {
			const saving = deferred<boolean>();
			vi.mocked(api.egress.setRule).mockReturnValue(saving.promise);
			await open();

			await userEvent.click(ruleRadio('api.test', 'block'));

			expect(rule('api.test')).toBe('block');
			saving.resolve(true);
		});

		it('puts the rule back and says why when refused', async () => {
			vi.mocked(api.egress.setRule).mockRejectedValue(new Error('gateway refused'));
			await open();

			await userEvent.click(ruleRadio('api.test', 'block'));

			const alert = await screen.findByRole('alert');
			expect(alert).toHaveTextContent('The rule did not change.');
			expect(alert).toHaveTextContent('gateway refused');
			expect(within(alert).queryByRole('button', { name: /Retry/ })).not.toBeInTheDocument();
			expect(rule('api.test')).toBe('allow');
			expect(toasts.list).toHaveLength(0);
		});

		it('brings held back as a choice when the first review is refused', async () => {
			vi.mocked(api.egress.setRule).mockRejectedValue(new Error('gateway refused'));
			await open([anAuditRow()], aPolicy());

			await userEvent.click(ruleRadio('api.test', 'allow'));
			await screen.findByRole('alert');

			expect(rule('api.test')).toBe('held');
		});

		it('clears the refusal on dismiss, keeping the rows', async () => {
			vi.mocked(api.egress.setRule).mockRejectedValue(new Error('gateway refused'));
			await open();
			await userEvent.click(ruleRadio('api.test', 'block'));

			await userEvent.click(await screen.findByRole('button', { name: 'Dismiss' }));

			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
			expect(screen.getByText('api.test')).toBeInTheDocument();
		});

		it('does nothing when held is clicked', async () => {
			await open([anAuditRow()], aPolicy());
			await userEvent.click(ruleRadio('api.test', 'held'));
			expect(api.egress.setRule).not.toHaveBeenCalled();
		});

		it('does nothing when the rule in force is clicked', async () => {
			await open();
			await userEvent.click(ruleRadio('api.test', 'allow'));
			expect(api.egress.setRule).not.toHaveBeenCalled();
		});

		it('takes no second rule from the keyboard while one is saving', async () => {
			vi.mocked(api.egress.setRule).mockReturnValue(deferred<boolean>().promise);
			await open([anAuditRow()], aPolicy());
			await userEvent.click(ruleRadio('api.test', 'allow'));

			await fireEvent.keyDown(group('api.test'), { key: 'ArrowRight' });

			expect(api.egress.setRule).toHaveBeenCalledOnce();
		});

		// The rule was saved: only reading it back failed.
		it('does not say the rule is unchanged when only the read-back failed', async () => {
			settle();
			await open();
			vi.mocked(api.egress.policy).mockRejectedValue(new Error('blip'));

			await userEvent.click(ruleRadio('api.test', 'block'));
			await vi.waitFor(() => expect(api.egress.policy).toHaveBeenCalledTimes(2));
			await tick();

			expect(screen.queryByText('The rule did not change.')).not.toBeInTheDocument();
			await vi.waitFor(() => expect(toasts.list).toHaveLength(1));
		});
	});

	describe('filtering', () => {
		const mixed = [
			anAuditRow({ host: 'ok.test', dstIp: '10.0.0.1' }),
			anAuditRow({ host: 'new.test', dstIp: '10.0.0.2' }),
			anAuditRow({ host: 'bad.test', dstIp: '10.0.0.3' })
		];
		const policy = aPolicy({ domains: ['ok.test'], blockedDomains: ['bad.test'] });

		it('counts the rows under each rule', async () => {
			await open(mixed, policy);
			expect(filter(/^All\d/)).toHaveTextContent('All3');
			expect(filter(/^Allowed/)).toHaveTextContent('Allowed1');
			expect(filter(/^Held/)).toHaveTextContent('Held1');
			expect(filter(/^Blocked/)).toHaveTextContent('Blocked1');
		});

		it('shows the rows under the chosen rule', async () => {
			const { rs } = await open(mixed, policy);
			await userEvent.click(filter(/^Blocked/));

			expect(rs.egressRule).toBe('blocked');
			expect(hosts()).toEqual(['bad.test']);
		});

		it('opens on the rule the banner asked for', async () => {
			await open(mixed, policy, aViewStore(aLiveRun(), { egressRule: 'held' }));
			expect(hosts()).toEqual(['new.test']);
		});

		it('moves a row out of the filter as soon as its rule flips', async () => {
			vi.mocked(api.egress.setRule).mockReturnValue(deferred<boolean>().promise);
			await open(mixed, policy, aViewStore(aLiveRun(), { egressRule: 'held' }));

			await userEvent.click(ruleRadio('new.test', 'allow'));

			expect(hosts()).toEqual([]);
			expect(filter(/^Allowed/)).toHaveTextContent('Allowed2');
		});

		it('filters by what was seen when there is no live gateway', async () => {
			const seen = [
				anAuditRow({ host: 'ok.test' }),
				anAuditRow({ host: 'bad.test', dstIp: '10.0.0.3', verdict: 'deny' })
			];
			await open(seen, null);
			await userEvent.click(filter(/^Blocked/));

			expect(hosts()).toEqual(['bad.test']);
			expect(filter(/^Held/)).toHaveTextContent('Held0');
		});

		it('searches hosts whatever the case', async () => {
			await open(mixed, policy);
			await userEvent.type(screen.getByRole('searchbox', { name: 'Filter host' }), ' OK.T ');
			expect(hosts()).toEqual(['ok.test']);
		});

		it('says when nothing matches, keeping the header', async () => {
			await open(mixed, policy);
			await userEvent.type(screen.getByRole('searchbox', { name: 'Filter host' }), 'zzz');

			expect(screen.getByText('Nothing matches.')).toBeInTheDocument();
			expect(screen.getByText('Host:port')).toBeInTheDocument();
		});
	});

	describe('a row', () => {
		it('hides the usual port and shows any other', async () => {
			await open([
				anAuditRow({ host: 'tls.test', port: 443 }),
				anAuditRow({ host: 'web.test', port: 8080 })
			]);
			expect(screen.getByText('tls.test')).toBeInTheDocument();
			expect(screen.getByText('web.test:8080')).toBeInTheDocument();
		});

		it('keeps what was seen apart from the rule now', async () => {
			await open([anAuditRow({ verdict: 'deny' })]);
			expect(screen.getByTitle(/^What happened to this traffic/)).toHaveTextContent('blocked');
			expect(rule('api.test')).toBe('allow');
		});

		it('gives the address it reached in the host title, when known', async () => {
			await open([anAuditRow({ dstIp: '' })]);
			expect(screen.getByTitle('api.test')).toBeInTheDocument();
		});

		it('states the protocol, count, traffic and age', async () => {
			await open([anAuditRow({ lastSeen: new Date(NOW - 180_000).toISOString() })]);
			expect(screen.getByText('tls')).toBeInTheDocument();
			expect(screen.getByText('3')).toBeInTheDocument();
			expect(screen.getByText('2 KB/4 KB')).toBeInTheDocument();
			expect(screen.getByText('3m ago')).toBeInTheDocument();
		});

		it('reads zero bytes as zero', async () => {
			await open([anAuditRow({ bytesOut: 0, bytesIn: 0 })]);
			expect(screen.getByText('0 B/0 B')).toBeInTheDocument();
		});
	});

	describe('the footer', () => {
		it('links the workshop by name and states the mode', async () => {
			fleet.projects = [aProject({ id: 'p1', name: 'Payments' })];
			const rs = aViewStore(aLiveRun({ projectId: 'p1' }));
			await open([anAuditRow()], aPolicy({ mode: 'deny' }), rs);

			const link = screen.getByRole('link', { name: 'Payments › workspace policy' });
			expect(link).toHaveAttribute('href', to.workshopSettings('p1'));
			expect(link.parentElement).toHaveTextContent(
				'Rule changes apply live and are saved to Payments › workspace policy (denylist).'
			);
		});

		it('falls back to the workshop while its name is unknown', async () => {
			await open([], aPolicy(), aViewStore(aLiveRun({ projectId: 'p1' })));
			expect(screen.getByRole('link', { name: 'the workshop › workspace policy' })).toBeVisible();
		});

		it('states no mode without a gateway', async () => {
			const rs = aViewStore(aRun({ projectId: 'p1' }));
			await open([], null, rs);
			expect(screen.getByRole('link').parentElement).toHaveTextContent(
				/saved to the workshop › workspace policy\.$/
			);
		});

		it('links nothing on a run outside a workshop', async () => {
			await open([], aPolicy());
			expect(
				screen.getByText(
					"Rule changes apply live and are saved to the workshop's policy (allowlist)."
				)
			).toBeInTheDocument();
			expect(screen.queryByRole('link')).not.toBeInTheDocument();
		});
	});

	describe('polling', () => {
		beforeEach(() => {
			vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
		});

		it('does nothing while hidden', () => {
			render(EgressView, { rs: aViewStore(aLiveRun()), visible: false });
			expect(api.egress.runAudit).not.toHaveBeenCalled();
		});

		it('reads audit and policy every 5s on a live run', async () => {
			await open();
			await vi.advanceTimersByTimeAsync(10_000);

			expect(api.egress.runAudit).toHaveBeenCalledTimes(3);
			expect(api.egress.policy).toHaveBeenCalledTimes(3);
		});

		it('picks up a rule changed elsewhere', async () => {
			await open();
			vi.mocked(api.egress.policy).mockResolvedValue(aPolicy({ blockedDomains: ['api.test'] }));

			await vi.advanceTimersByTimeAsync(5000);

			expect(rule('api.test')).toBe('block');
		});

		it('skips while the tab is in the background', async () => {
			await open();
			vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);

			await vi.advanceTimersByTimeAsync(5000);

			expect(api.egress.runAudit).toHaveBeenCalledOnce();
		});

		it('stops when the window hides', async () => {
			const { rerender } = await open();
			await rerender({ visible: false });

			await vi.advanceTimersByTimeAsync(10_000);

			expect(api.egress.runAudit).toHaveBeenCalledOnce();
		});

		it('reads a finished run once', async () => {
			await open([anAuditRow()], null, aViewStore(aRun({ state: 'failed' })));
			await vi.advanceTimersByTimeAsync(10_000);
			expect(api.egress.runAudit).toHaveBeenCalledOnce();
		});
	});
});
