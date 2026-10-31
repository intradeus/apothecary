// What the run page's core tests need beyond ./run-views and ./run-components.
// A `.svelte.ts` so that form values are a real rune: PingCard binds them with
// $state, and a plain object would not re-render what a field writes to it.
import type { FormValue, PingField, PingForm } from '$lib/domain';

/** Form values as PingCard holds them. */
export const formValues = (initial: Record<string, FormValue | undefined> = {}) => {
	const values = $state(initial);
	return values;
};

export const aField = (over: Partial<PingField> & { id: string }): PingField => ({
	type: 'string',
	required: false,
	options: [],
	...over
});

export const aForm = (...fields: (Partial<PingField> & { id: string })[]): PingForm => ({
	fields: fields.map(aField)
});

export const options = (...ids: string[]) => ids.map((id) => ({ id, label: id.toUpperCase() }));

/** A picked file, as the browser hands it over. */
export const aFile = (name = 'notes.txt', type = 'text/plain', bytes = 5): File =>
	new File(['x'.repeat(bytes)], name, { type });
