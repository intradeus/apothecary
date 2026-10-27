// The transport half of the anti-corruption layer.
//
// Everything the generated SDK returns passes through unwrap() before anything
// else sees it: one place turns a transport result into a value or an Error
// with the engine's own message, so no component ever handles a
// { data, error, response } triple.

import { client } from './generated/client.gen';
import { t } from '$lib/i18n/index.svelte';

// Same origin: the engine serves the UI, and the dev server proxies /api.
// The session cookie rides along on every call by itself.
client.setConfig({ baseUrl: '' });

/** Called when the engine answers 401: the session is gone (signed out
 *  elsewhere, expired, the account disabled). The session store registers
 *  itself here rather than being imported, so the transport stays below it. */
let unauthorized: (() => void) | null = null;
export function onUnauthorized(fn: () => void) {
	unauthorized = fn;
}
client.interceptors.response.use((response) => {
	if (response.status === 401) unauthorized?.();
	return response;
});

/** The shape every generated SDK call resolves to: data or error, plus the
 *  raw exchange. Declared structurally rather than imported, so the ACL does
 *  not take a type dependency on the generated client's internals. */
type SdkResult<T> = ({ data: T; error: undefined } | { data: undefined; error: unknown }) & {
	request?: Request;
	response?: Response;
};

/** Pull the engine's error message out of whatever the failure carried. */
function messageOf(error: unknown, response?: Response): string {
	if (error && typeof error === 'object' && 'error' in error) {
		const msg = (error as { error?: unknown }).error;
		if (typeof msg === 'string' && msg) return msg;
	}
	if (typeof error === 'string' && error) return error;
	if (response) return `${response.status} ${response.statusText}`.trim();
	return t('api.unreachable');
}

/** Resolve a call to its body, or throw the engine's message. */
export async function unwrap<T>(call: Promise<SdkResult<T>>): Promise<T> {
	const { data, error, response } = await call;
	if (error !== undefined || !response?.ok) throw new Error(messageOf(error, response));
	return data as T;
}

/** Resolve a call that returns nothing (202/204). */
export async function unwrapVoid(call: Promise<SdkResult<unknown>>): Promise<void> {
	const { error, response } = await call;
	if (error !== undefined || !response?.ok) throw new Error(messageOf(error, response));
}

/** Resolve a call whose failure is a legitimate "not available here" answer
 *  rather than an error to show: a run with no gateway, a provisioner without
 *  log tailing. */
export async function unwrapOptional<T>(call: Promise<SdkResult<T>>): Promise<T | null> {
	const { data, error, response } = await call;
	if (error !== undefined || !response?.ok) return null;
	return (data ?? null) as T | null;
}
