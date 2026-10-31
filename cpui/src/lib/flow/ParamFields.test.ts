import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { aParam, fill, pick } from '$test/fixtures/flow.svelte';
import type { ParamDecl } from '$lib/domain';
import ParamFields from './ParamFields.svelte';

type Props = Partial<Parameters<typeof ParamFields>[1]>;

const setup = (p: ParamDecl | ParamDecl[], over: Props = {}) => {
	const onchange = vi.fn();
	render(ParamFields, {
		params: Array.isArray(p) ? p : [p],
		values: {},
		onchange,
		...over
	});
	return { onchange };
};

describe('ParamFields', () => {
	describe('what applies when it is left empty', () => {
		it('is the flow’s default', () => {
			setup(aParam('region', { default: 'eu' }));
			expect(screen.getByText('flow default: eu')).toBeInTheDocument();
			expect(screen.getByLabelText(/^region/)).toHaveAttribute('placeholder', 'eu');
		});

		it('is the workshop’s value when there is one', () => {
			setup(aParam('region', { default: 'eu' }), { under: { region: 'us' } });
			expect(screen.getByText('workshop: us')).toBeInTheDocument();
			expect(screen.getByLabelText(/^region/)).toHaveAttribute('placeholder', 'us');
		});

		it('is the run’s value on a retry', () => {
			setup(aParam('region'), { under: { region: 'us' }, underLabel: 'run' });
			expect(screen.getByText('this run: us')).toBeInTheDocument();
		});

		it('is another parameter’s, when its default reads one', () => {
			setup(aParam('reviewer', { defaultRef: 'model' }));
			expect(screen.getByText('follows $model unless set')).toBeInTheDocument();
		});

		it('is nothing yet for a parameter that waits', () => {
			setup(aParam('model', { required: true }));
			expect(screen.getByText(/no value yet/)).toBeInTheDocument();
		});
	});

	describe('each kind gets its control', () => {
		it('text', async () => {
			const { onchange } = setup(aParam('region', { description: 'Where it runs.' }));
			expect(screen.getByText('Where it runs.')).toBeInTheDocument();
			await fill(screen.getByLabelText(/^region/), 'x');
			expect(onchange).toHaveBeenLastCalledWith('region', 'x');
		});

		it('a bool', async () => {
			const { onchange } = setup(aParam('on', { type: 'bool' }));
			await pick(/^on/, 'true');
			expect(onchange).toHaveBeenCalledExactlyOnceWith('on', 'true');
		});

		it('an enum', async () => {
			const { onchange } = setup(aParam('mode', { enum: ['fast', 'slow'] }));
			await pick(/^mode/, 'slow');
			expect(onchange).toHaveBeenCalledExactlyOnceWith('mode', 'slow');
		});

		it('an enum left unset', async () => {
			const { onchange } = setup(aParam('mode', { enum: ['fast'] }), { values: { mode: 'fast' } });
			await pick(/^mode/, '— not set here');
			expect(onchange).toHaveBeenCalledExactlyOnceWith('mode', '');
		});

		it('an int, as a number', async () => {
			const { onchange } = setup(aParam('n', { type: 'int' }));
			const field = screen.getByLabelText(/^n/);
			expect(field).toHaveAttribute('step', '1');
			await fill(field, '4');
			expect(onchange).toHaveBeenLastCalledWith('n', '4');
		});

		it('a float, taking any step', () => {
			setup(aParam('n', { type: 'float' }));
			expect(screen.getByLabelText(/^n/)).toHaveAttribute('step', 'any');
		});

		it('a duration, cleared by a button that shows while it is set', async () => {
			const { onchange } = setup(aParam('t', { type: 'duration' }), { values: { t: '2h' } });
			await userEvent.click(screen.getByRole('button', { name: 'Clear' }));
			expect(onchange).toHaveBeenCalledExactlyOnceWith('t', '');
		});

		it('a duration unset has nothing to clear', () => {
			setup(aParam('t', { type: 'duration' }));
			expect(screen.queryByRole('button', { name: 'Clear' })).not.toBeInTheDocument();
		});
	});

	describe('a harness', () => {
		const harnesses = [{ kind: 'omp', label: 'Oh my pi' }];

		it('is picked from what the workshop can run', async () => {
			const { onchange } = setup(aParam('harness', { role: 'harness' }), { harnesses });
			await pick(/^harness/, 'Oh my pi');
			expect(onchange).toHaveBeenCalledExactlyOnceWith('harness', 'omp');
		});

		it('keeps a value the workshop cannot run, so it still reads', () => {
			setup(aParam('harness', { role: 'harness' }), { harnesses, values: { harness: 'old' } });
			expect(screen.getByRole('combobox', { name: /^harness/ })).toHaveTextContent('old');
		});
	});

	describe('a model', () => {
		const modelsByKind = {
			omp: [{ id: 'acme/fast', name: 'Fast' }]
		} as unknown as Props['modelsByKind'];
		const model = aParam('model', { role: 'model', harness: '$harness' });
		const harness = aParam('harness', { role: 'harness' });

		it('suggests what the harness chosen on the form lists', () => {
			setup([harness, model], { values: { harness: 'omp' }, modelsByKind });
			expect(screen.getByText('suggestions: what harness omp lists')).toBeInTheDocument();
			expect(document.querySelector('#pf-model-list option')).toHaveAttribute('value', 'acme/fast');
		});

		it('reads the harness from the layer underneath', () => {
			setup([harness, model], { under: { harness: 'omp' }, modelsByKind });
			expect(document.querySelector('#pf-model-list option')).toHaveAttribute('value', 'acme/fast');
		});

		it('finds the harness among all the flow’s parameters', () => {
			setup(model, { all: [harness, model], values: { harness: 'omp' }, modelsByKind });
			expect(document.querySelector('#pf-model-list option')).not.toBeNull();
		});

		it('asks for the harness first', () => {
			setup([harness, model]);
			expect(screen.getByText('pick the harness first to see its models')).toBeInTheDocument();
		});

		it('writes the id trimmed', async () => {
			const { onchange } = setup(model);
			await fill(screen.getByLabelText(/^model/), ' acme/fast ');
			expect(onchange).toHaveBeenLastCalledWith('model', 'acme/fast');
		});
	});

	describe('a list', () => {
		it('writes typed items as JSON', async () => {
			const { onchange } = setup(aParam('tags', { list: true }));
			await fill(screen.getByLabelText(/^tags/), 'a, b');
			expect(onchange).toHaveBeenLastCalledWith('tags', '["a","b"]');
		});

		it('writes nothing when emptied', async () => {
			const { onchange } = setup(aParam('tags', { list: true }), { values: { tags: '["a"]' } });
			await fill(screen.getByLabelText(/^tags/), '');
			expect(onchange).toHaveBeenLastCalledWith('tags', '');
		});

		it('shows a list’s JSON as its items', () => {
			setup(aParam('tags', { list: true, default: '["x","y"]' }), {
				values: { tags: '["a","b"]' }
			});
			expect(screen.getByLabelText(/^tags/)).toHaveValue('a, b');
			expect(screen.getByLabelText(/^tags/)).toHaveAttribute('placeholder', 'x, y');
			expect(screen.getByText('flow default: x, y')).toBeInTheDocument();
		});

		it('shows nothing for a value that is no list', () => {
			setup(aParam('tags', { list: true }), { values: { tags: 'not json' } });
			expect(screen.getByLabelText(/^tags/)).toHaveValue('');
		});

		describe('of allowed values', () => {
			const tags = aParam('tags', { list: true, enum: ['a', 'b', 'c'], default: '["a"]' });

			it('starts from what applies', () => {
				setup(tags);
				expect(screen.getByRole('checkbox', { name: 'a' })).toBeChecked();
				expect(screen.getByRole('checkbox', { name: 'b' })).not.toBeChecked();
			});

			it('adds to what applies, in the order the flow declares', async () => {
				const { onchange } = setup(tags, { values: { tags: '["c"]' } });
				await userEvent.click(screen.getByRole('checkbox', { name: 'a' }));
				expect(onchange).toHaveBeenCalledExactlyOnceWith('tags', '["a","c"]');
			});

			it('takes one off', async () => {
				const { onchange } = setup(tags);
				await userEvent.click(screen.getByRole('checkbox', { name: 'a' }));
				expect(onchange).toHaveBeenCalledExactlyOnceWith('tags', '[]');
			});

			it('offers to clear only what is set here', async () => {
				const { onchange } = setup(tags, { values: { tags: '["b"]' } });
				await userEvent.click(screen.getByRole('button', { name: 'Clear' }));
				expect(onchange).toHaveBeenCalledExactlyOnceWith('tags', '');
			});

			it('has nothing to clear unset', () => {
				setup(tags);
				expect(screen.queryByRole('button', { name: 'Clear' })).not.toBeInTheDocument();
			});
		});
	});

	it('keeps ids apart between forms', () => {
		setup(aParam('region'), { idPrefix: 'run' });
		expect(screen.getByLabelText(/^region/)).toHaveAttribute('id', 'run-region');
	});
});
