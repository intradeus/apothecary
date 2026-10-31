import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { ServiceConfig } from '$lib/domain';
import { bindable } from '$test/fixtures/app.svelte';
import ServicesEditor, {
	blankService,
	serviceConfigs,
	serviceIssues,
	serviceRows,
	type ServiceRow
} from './ServicesEditor.svelte';

const row = (over: Partial<ServiceRow> = {}): ServiceRow => ({
	...blankService('web'),
	start: 'npm start',
	...over
});
const issues = (r: ServiceRow, ...others: ServiceRow[]) => serviceIssues([r, ...others]).get(r.key);

describe('serviceRows', () => {
	const config = (over: Partial<ServiceConfig> = {}): ServiceConfig => ({
		name: 'web',
		start: 'go',
		ports: [],
		...over
	});

	it('reads the probe the engine stored', () => {
		const [http, tcp, cmd, none] = serviceRows([
			config({ health: { http: 'http://x' } }),
			config({ health: { tcp: 80 } }),
			config({ health: { command: 'true' } }),
			config()
		]);
		expect(http.probe).toBe('http');
		expect(tcp.probe).toBe('tcp');
		expect(cmd.probe).toBe('command');
		expect(none.probe).toBe('none');
	});

	it('gives each row its own key', () => {
		const [a, b] = serviceRows([config(), config()]);
		expect(a.key).not.toBe(b.key);
	});

	it('fills what is missing with empty fields', () => {
		const [r] = serviceRows([config({ ports: [{ port: 80 }] })]);
		expect(r).toMatchObject({ description: '', stop: '', http: '', tcp: '', timeout: '' });
		expect(r.ports).toEqual([{ port: 80, label: '', path: '' }]);
	});
});

describe('serviceConfigs', () => {
	it('round-trips what the engine sent', () => {
		const stored: ServiceConfig[] = [
			{
				name: 'web',
				description: 'The app',
				start: 'npm start',
				stop: 'npm stop',
				health: { http: 'http://localhost/health', timeout: '5m', interval: '10s' },
				ports: [{ port: 3000, label: 'web', path: '/' }]
			}
		];
		expect(serviceConfigs(serviceRows(stored))).toEqual(stored);
	});

	it('sends only the chosen probe', () => {
		const [c] = serviceConfigs([
			row({ probe: 'tcp', tcp: 5432, http: 'http://left-over', command: 'left over' })
		]);
		expect(c.health).toEqual({ tcp: 5432, timeout: undefined, interval: undefined });
	});

	it('sends no durations without a probe', () => {
		const [c] = serviceConfigs([row({ timeout: '5m', interval: '10s' })]);
		expect(c.health).toBeUndefined();
	});

	it('treats a blank probe as none', () => {
		const [http, cmd, tcp] = serviceConfigs([
			row({ probe: 'http', http: '  ' }),
			row({ probe: 'command', command: ' ' }),
			row({ probe: 'tcp', tcp: '' })
		]);
		expect([http.health, cmd.health, tcp.health]).toEqual([undefined, undefined, undefined]);
	});

	it('trims names and leaves scripts as written', () => {
		const [c] = serviceConfigs([row({ name: ' web ', start: '  npm start\n', stop: '  ' })]);
		expect(c).toMatchObject({ name: 'web', start: '  npm start\n', stop: undefined });
	});

	it('drops an entirely empty port row and keeps a half-filled one', () => {
		const [c] = serviceConfigs([
			row({
				ports: [
					{ port: '', label: '', path: '' },
					{ port: 3000, label: ' web ', path: '' }
				]
			})
		]);
		expect(c.ports).toEqual([{ port: 3000, label: 'web', path: undefined }]);
	});
});

describe('serviceIssues', () => {
	it('passes a good service', () => {
		expect(serviceIssues([row()]).size).toBe(0);
	});

	it.each(['', 'Web', '1st', 'a b'])('refuses the name %j', (name) => {
		expect(issues(row({ name }))).toContain('name');
	});

	it('flags both of two services with one name', () => {
		const a = row();
		const b = row();
		expect(issues(a, b)).toContain('dup');
		expect(serviceIssues([a, b]).get(b.key)).toContain('dup');
	});

	it('wants a start script', () => {
		expect(issues(row({ start: '  ' }))).toEqual(['start']);
	});

	it.each(['', 'localhost:3000', 'ftp://x', 'http://'])('refuses the probe URL %j', (http) => {
		expect(issues(row({ probe: 'http', http }))).toContain('http');
	});

	it('takes an http or https probe URL', () => {
		expect(issues(row({ probe: 'http', http: 'https://x:1/health' }))).toBeUndefined();
	});

	it.each(['' as const, 0, 65536, 1.5])('refuses the probe port %j', (tcp) => {
		expect(issues(row({ probe: 'tcp', tcp }))).toContain('tcp');
	});

	it('wants a probe script', () => {
		expect(issues(row({ probe: 'command', command: ' ' }))).toContain('command');
	});

	it('checks durations only with a probe', () => {
		expect(issues(row({ timeout: 'soon' }))).toBeUndefined();
		const bad = issues(row({ probe: 'command', command: 'x', timeout: 'soon', interval: '0s' }));
		expect(bad).toEqual(['timeout', 'interval']);
	});

	it('flags a bad port by its row', () => {
		const bad = issues(
			row({
				ports: [
					{ port: 80, label: '', path: '' },
					{ port: 70000, label: '', path: '' },
					{ port: 80, label: '', path: '' },
					{ port: 81, label: '', path: 'nope' }
				]
			})
		);
		expect(bad).toEqual(['port1', 'portdup2', 'path3']);
	});

	it('flags a port row with a label but no port, and ignores an empty one', () => {
		expect(issues(row({ ports: [{ port: '', label: 'web', path: '' }] }))).toEqual(['port0']);
		expect(issues(row({ ports: [{ port: '', label: '', path: '' }] }))).toBeUndefined();
	});
});

describe('ServicesEditor', () => {
	const setup = (rows: ServiceRow[] = [], strict = false) => {
		const props = bindable({
			rows,
			strict,
			get issues() {
				return serviceIssues(this.rows);
			}
		});
		render(ServicesEditor, props);
		return props;
	};

	it('offers the first service as default', async () => {
		const props = setup();
		await userEvent.click(screen.getByRole('button', { name: /Add a service/ }));
		expect(props.rows).toHaveLength(1);
		expect(props.rows[0].name).toBe('default');
		expect(screen.getByLabelText('Name')).toHaveValue('default');
	});

	it('leaves the next one for naming', async () => {
		const props = setup([row()]);
		await userEvent.click(screen.getByRole('button', { name: /Add another service/ }));
		expect(props.rows[1].name).toBe('');
	});

	it('opens a lone service and folds several', () => {
		const { unmount } = render(ServicesEditor, { rows: [row()], issues: new Map() });
		expect(screen.getByLabelText('Name')).toBeInTheDocument();
		unmount();

		render(ServicesEditor, { rows: [row(), row({ name: 'db' })], issues: new Map() });
		expect(screen.queryByLabelText('Name')).not.toBeInTheDocument();
	});

	it('sums up a folded service', () => {
		render(ServicesEditor, {
			rows: [
				row({
					start: 'npm start\nmore',
					probe: 'tcp',
					tcp: 80,
					ports: [{ port: 3000, label: '', path: '' }]
				}),
				row({ name: 'db' })
			],
			issues: new Map()
		});
		expect(screen.getAllByText('npm start')).not.toHaveLength(0);
		expect(screen.getByText('· tcp 80')).toBeInTheDocument();
		expect(screen.getByText(':3000')).toBeInTheDocument();
	});

	it('folds and unfolds', async () => {
		render(ServicesEditor, { rows: [row(), row({ name: 'db' })], issues: new Map() });
		await userEvent.click(screen.getByRole('button', { name: 'Unfold web' }));
		expect(screen.getByLabelText('Name')).toHaveValue('web');

		await userEvent.click(screen.getByRole('button', { name: 'Fold web' }));
		expect(screen.queryByLabelText('Name')).not.toBeInTheDocument();
	});

	it('opens a service with something to fix on its own', () => {
		const r = row({ name: 'Bad' });
		setup([r, row({ name: 'db' })]);
		expect(screen.getByLabelText('Name')).toHaveValue('Bad');
		expect(screen.getByText('to fix')).toBeInTheDocument();
	});

	it('does not scold an empty start until a save was tried', () => {
		const r = row({ start: '' });
		const { unmount } = render(ServicesEditor, { rows: [r], issues: serviceIssues([r]) });
		expect(screen.queryByText(/Start is required/)).not.toBeInTheDocument();
		expect(screen.queryByText('to fix')).not.toBeInTheDocument();
		unmount();

		render(ServicesEditor, { rows: [r], issues: serviceIssues([r]), strict: true });
		expect(screen.getByText(/Start is required/)).toBeInTheDocument();
		expect(screen.getByText('to fix')).toBeInTheDocument();
	});

	it('edits through to the rows', async () => {
		const props = setup([row({ name: '' })]);
		await userEvent.type(screen.getByLabelText('Name'), 'api');
		await userEvent.type(screen.getByLabelText('Description'), 'The API');
		await userEvent.type(screen.getByLabelText(/^Stop/), 'kill');
		expect(props.rows[0]).toMatchObject({ name: 'api', description: 'The API', stop: 'kill' });
	});

	it('switches the probe, showing its field and durations', async () => {
		const props = setup([row()]);
		expect(screen.getByText(/No probe: up as soon as it started/)).toBeInTheDocument();

		await userEvent.click(screen.getByRole('radio', { name: 'TCP' }));
		await userEvent.type(screen.getByLabelText('Port'), '5432');
		await userEvent.type(screen.getByLabelText('Timeout'), '30s');

		expect(props.rows[0]).toMatchObject({ probe: 'tcp', tcp: 5432, timeout: '30s' });
	});

	it('shows errors under the probe fields', async () => {
		setup([row({ probe: 'http', http: 'nope', timeout: 'soon' })]);
		expect(screen.getByText('A duration: 30s, 5m, 1h30m.')).toBeInTheDocument();
		expect(screen.getByLabelText('URL')).toHaveClass('invalid');
	});

	it('adds, edits and removes ports', async () => {
		const props = setup([row()]);
		await userEvent.click(screen.getByRole('button', { name: /Add port/ }));
		await userEvent.type(screen.getByRole('spinbutton', { name: 'port' }), '3000');
		await userEvent.type(screen.getByRole('textbox', { name: 'label' }), 'web');
		expect(props.rows[0].ports).toEqual([{ port: 3000, label: 'web', path: '' }]);

		await userEvent.click(screen.getByRole('button', { name: /Remove port/ }));
		expect(props.rows[0].ports).toEqual([]);
	});

	// t() groups numbers, and a port is an identifier.
	it('names a port as it was written', () => {
		setup([
			row({
				ports: [
					{ port: 3000, label: '', path: '' },
					{ port: 3000, label: '', path: '' }
				]
			})
		]);
		expect(screen.getByText('Port 3000 is listed twice.')).toBeInTheDocument();
		expect(screen.getAllByRole('button', { name: 'Remove port 3000' })).toHaveLength(2);
	});

	it('names port errors', async () => {
		setup([
			row({
				ports: [
					{ port: 80, label: '', path: 'x' },
					{ port: 80, label: '', path: '' }
				]
			})
		]);
		expect(screen.getByText('Port 80 is listed twice.')).toBeInTheDocument();
		expect(screen.getByText('A path starts with /.')).toBeInTheDocument();
	});

	it('reorders, stopping at the ends', async () => {
		const props = setup([row({ name: 'a' }), row({ name: 'b' })]);
		expect(screen.getByRole('button', { name: 'Move a up' })).toBeDisabled();
		expect(screen.getByRole('button', { name: 'Move b down' })).toBeDisabled();

		await userEvent.click(screen.getByRole('button', { name: 'Move b up' }));
		expect(props.rows.map((r) => r.name)).toEqual(['b', 'a']);

		await userEvent.click(screen.getByRole('button', { name: 'Move b down' }));
		expect(props.rows.map((r) => r.name)).toEqual(['a', 'b']);
	});

	it('removes a service', async () => {
		const props = setup([row({ name: 'a' }), row({ name: 'b' })]);
		await userEvent.click(screen.getByRole('button', { name: 'Remove a' }));
		expect(props.rows.map((r) => r.name)).toEqual(['b']);
	});

	it('focuses the first field worth typing in', async () => {
		setup();
		await userEvent.click(screen.getByRole('button', { name: /Add a service/ }));
		await waitFor(() => expect(screen.getByLabelText('Start')).toHaveFocus());
	});

	it('calls an unnamed service unnamed', () => {
		setup([row({ name: '' })]);
		expect(screen.getByRole('button', { name: 'Remove unnamed' })).toBeInTheDocument();
		expect(screen.getByText('unnamed')).toBeInTheDocument();
	});
});
