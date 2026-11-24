import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { bindable } from '$test/fixtures/app.svelte';
import { MAX_ENV_VARS, type EnvVarRow } from '$lib/domain';
import WorkspaceEnvField from './WorkspaceEnvField.svelte';

const rows = (...r: [string, string][]): EnvVarRow[] => r.map(([name, value]) => ({ name, value }));

describe('WorkspaceEnvField', () => {
	it('says the values are not secret and points credentials to Secrets', () => {
		render(WorkspaceEnvField, bindable({ rows: [] as EnvVarRow[] }));
		expect(screen.getByText(/never put a credential here: use Secrets/)).toBeInTheDocument();
		expect(screen.getByText('${APO_CPUS}', { selector: 'code' })).toBeInTheDocument();
		expect(screen.queryByRole('table')).not.toBeInTheDocument();
	});

	it('adds, edits and removes rows through the binding', async () => {
		const props = bindable({ rows: rows(['JEST_MAX_WORKERS', '2']) });
		render(WorkspaceEnvField, props);
		expect(screen.getByRole('textbox', { name: 'Value of JEST_MAX_WORKERS' })).toHaveValue('2');

		await userEvent.click(screen.getByRole('button', { name: /Add a variable/ }));
		const names = screen.getAllByRole('textbox', { name: 'Variable name' });
		await userEvent.type(names[1], 'NX_PARALLEL');
		await userEvent.type(
			screen.getByRole('textbox', { name: 'Value of NX_PARALLEL' }),
			'${{APO_CPUS}'
		);
		expect(props.rows).toEqual(rows(['JEST_MAX_WORKERS', '2'], ['NX_PARALLEL', '${APO_CPUS}']));

		await userEvent.click(screen.getByRole('button', { name: 'Remove JEST_MAX_WORKERS' }));
		expect(props.rows).toEqual(rows(['NX_PARALLEL', '${APO_CPUS}']));
	});

	it('says beside a row what the engine would refuse', () => {
		render(
			WorkspaceEnvField,
			bindable({
				rows: rows(
					['APO_CPUS', '8'],
					['PATH', '/bin'],
					['A', '1'],
					['A', '2'],
					['1X', ''],
					['', '']
				)
			})
		);
		expect(screen.getByText(/starting with APO_ are the engine’s own/)).toBeInTheDocument();
		expect(screen.getByText(/sets this in every workspace itself/)).toBeInTheDocument();
		expect(screen.getByText(/Set twice/)).toBeInTheDocument();
		expect(screen.getByText(/Not a variable name/)).toBeInTheDocument();
		// The unfinished last row is no issue, and is named as such.
		expect(screen.getByRole('button', { name: 'Remove this variable' })).toBeInTheDocument();
		const invalid = screen
			.getAllByRole('textbox', { name: 'Variable name' })
			.map((i) => i.getAttribute('aria-invalid'));
		expect(invalid).toEqual(['true', 'true', 'false', 'true', 'true', 'false']);
	});

	it('says a value is too long, and when there are too many', () => {
		const many = Array.from({ length: MAX_ENV_VARS + 1 }, (_, i): [string, string] => [
			`V${i}`,
			'x'
		]);
		render(
			WorkspaceEnvField,
			bindable({ rows: [...rows(['BIG', 'x'.repeat(5000)]), ...rows(...many)] })
		);
		expect(screen.getByText(/at most 4,096 bytes/)).toBeInTheDocument();
		expect(screen.getByText(/At most 64 variables/)).toBeInTheDocument();
	});
});
