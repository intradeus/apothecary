import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { bound } from '$test/fixtures/designsystem.svelte';
import KeyValueEditor, { type KVRow } from './KeyValueEditor.svelte';

const setup = (rows: KVRow[], rest: Record<string, unknown> = {}) => {
	const b = bound('rows', rows, rest);
	render(KeyValueEditor, b.props);
	return b;
};
const keys = () => screen.queryAllByRole('textbox', { name: 'Key' }) as HTMLInputElement[];

describe('KeyValueEditor', () => {
	it('shows a key and a named value field for each row', () => {
		setup([
			{ key: 'PORT', value: '8080' },
			{ key: 'HOST', value: 'local' }
		]);

		expect(keys().map((k) => k.value)).toEqual(['PORT', 'HOST']);
		expect(screen.getByRole('textbox', { name: 'Value for PORT' })).toHaveValue('8080');
		expect(screen.getByRole('textbox', { name: 'Value for HOST' })).toHaveValue('local');
	});

	it('edits one row and leaves the others', async () => {
		const b = setup([
			{ key: 'A', value: '1' },
			{ key: 'B', value: '2' }
		]);

		await userEvent.type(screen.getByRole('textbox', { name: 'Value for B' }), '34');
		await userEvent.type(keys()[0], 'X');

		expect(b.current()).toEqual([
			{ key: 'AX', value: '1' },
			{ key: 'B', value: '234' }
		]);
	});

	it('keeps focus in the field across keystrokes', async () => {
		setup([{ key: '', value: '' }]);
		await userEvent.type(keys()[0], 'abc');
		expect(keys()[0]).toHaveFocus();
		expect(keys()[0]).toHaveValue('abc');
	});

	it('adds an empty row at the foot', async () => {
		const b = setup([{ key: 'A', value: '1' }], { addLabel: 'Add a header' });

		await userEvent.click(screen.getByRole('button', { name: /Add a header/ }));

		expect(b.current()).toEqual([
			{ key: 'A', value: '1' },
			{ key: '', value: '' }
		]);
		expect(keys()).toHaveLength(2);
	});

	it('removes exactly the row asked for', async () => {
		const b = setup([
			{ key: 'A', value: '1' },
			{ key: 'B', value: '2' },
			{ key: 'C', value: '3' }
		]);

		await userEvent.click(screen.getByRole('button', { name: 'Remove B' }));

		expect(b.current().map((r) => r.key)).toEqual(['A', 'C']);
		expect(keys().map((k) => k.value)).toEqual(['A', 'C']);
		expect(screen.getByRole('textbox', { name: 'Value for C' })).toHaveValue('3');
	});

	it('names the remove button of a row that has no key yet', () => {
		setup([{ key: '', value: '' }]);
		expect(screen.getByRole('button', { name: 'Remove row' })).toBeInTheDocument();
	});

	it('takes scripts in a textarea when multiline', () => {
		setup([{ key: 'build', value: 'make\nmake test' }], { multiline: true });
		const value = screen.getByRole('textbox', { name: 'Value for build' });
		expect(value.tagName).toBe('TEXTAREA');
		expect(value).toHaveValue('make\nmake test');
	});

	it('suggests values through a datalist', () => {
		setup([{ key: 'A', value: '' }], { suggestions: ['$WORKSPACE', '$MR_URL'] });

		const value = screen.getByRole('combobox', { name: 'Value for A' });
		const list = document.getElementById(value.getAttribute('list')!) as HTMLDataListElement;

		expect([...list.options].map((o) => o.value)).toEqual(['$WORKSPACE', '$MR_URL']);
	});

	it('offers no datalist without suggestions', () => {
		setup([{ key: 'A', value: '' }]);
		expect(screen.getByRole('textbox', { name: 'Value for A' })).not.toHaveAttribute('list');
	});

	it('marks only the keys it is told are invalid', () => {
		setup(
			[
				{ key: 'dup', value: '' },
				{ key: 'ok', value: '' }
			],
			{ invalid: new Set(['dup']) }
		);
		expect(keys()[0]).toHaveClass('invalid');
		expect(keys()[1]).not.toHaveClass('invalid');
	});

	it('passes its placeholders to the fields', () => {
		setup([{ key: '', value: '' }], { keyPlaceholder: 'header', valuePlaceholder: 'content' });
		expect(screen.getByPlaceholderText('header')).toBe(keys()[0]);
		expect(screen.getByPlaceholderText('content')).toBeInTheDocument();
	});

	it('still offers the add when there are no rows', () => {
		setup([]);
		expect(keys()).toHaveLength(0);
		expect(screen.getByRole('button', { name: /Add/ })).toBeInTheDocument();
	});
});
