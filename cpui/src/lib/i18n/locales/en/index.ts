// The English catalog — the source of truth for keys and their types.
//
// One file per area, merged here. Keys are flat, dotted and prefixed by area
// (`run.header.rerun`), so two areas can't collide; `merge` throws in dev if
// they do. Plurals: define `<key>_one` and `<key>_other` and call
// `t('<key>', { count })`. Params are `{name}` placeholders.

import common from './common';
import ui from './ui';
import runcore from './runcore';
import runviews from './runviews';
import routes from './routes';
import workshop from './workshop';
import shell from './shell';
import desk from './desk';
import misc from './misc';

const merge = <T extends Record<string, string>[]>(...parts: T) => {
	const out: Record<string, string> = {};
	for (const p of parts)
		for (const [k, v] of Object.entries(p)) {
			if (import.meta.env?.DEV && k in out) throw new Error(`i18n: duplicate key "${k}"`);
			out[k] = v;
		}
	return out as UnionToIntersection<T[number]>;
};
type UnionToIntersection<U> = (U extends unknown ? (x: U) => void : never) extends (
	x: infer I
) => void
	? I
	: never;

export const en = merge(common, ui, runcore, runviews, routes, workshop, shell, desk, misc);
export type MessageKey = keyof typeof en;
