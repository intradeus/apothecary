import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { text } from '$test/helpers';
import SettingRow from './SettingRow.svelte';

describe('SettingRow', () => {
	it('names the control it holds through forId', () => {
		const { container } = render(SettingRow, {
			label: 'Retention',
			forId: 'retention',
			children: text('Control')
		});
		expect(screen.getByText('Retention')).toHaveAttribute('for', 'retention');
		expect(container.querySelector('.ctl')).toHaveTextContent('Control');
	});

	it('explains the setting when given help', () => {
		render(SettingRow, { label: 'Retention', help: 'How long runs stay', children: text('x') });
		expect(screen.getByText('How long runs stay')).toBeInTheDocument();
	});

	it('adds the quieter note', () => {
		render(SettingRow, { label: 'Retention', note: text('Currently 48h'), children: text('x') });
		expect(screen.getByText('Currently 48h')).toBeInTheDocument();
	});

	it('leaves out help and note when there are none', () => {
		const { container } = render(SettingRow, { label: 'Retention', children: text('x') });
		expect(container.querySelector('.help')).toBeNull();
	});
});
