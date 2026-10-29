// Model provider credentials (docs/model-providers-plan.md): a person's API
// keys and logins, which their harness integrations reference. The UI only
// ever sees metadata — no key or token crosses the API.

/** One provider of the closed set apothecary manages credentials for. */
export interface ProviderInfo {
	id: string;
	name: string;
	/** It takes an API key. */
	key: boolean;
	/** It has a login apothecary keeps (and refreshes). */
	login: boolean;
	/** That login can be started from here (else: import a file). */
	signIn: boolean;
	/** Harness kinds that use it. */
	harnesses: string[];
}

export type CredentialKind = 'api_key' | 'oauth';

/** One credential, as shown. */
export interface Credential {
	id: string;
	provider: string;
	kind: CredentialKind;
	name: string;
	/** The signed-in account (a login's email), when known. */
	account?: string;
	/** A login's access token expiry; undefined = never. */
	expiresAt?: string;
	/** When the engine last refreshed the login. */
	lastRefresh?: string;
	/** ok, or disabled: a refresh failed for good — sign in again. */
	status: 'ok' | 'disabled';
	error?: string;
	createdAt: string;
}

/** A sign-in in progress. */
export interface SignInPending {
	id: string;
	provider: string;
	/** The provider page to open. */
	url: string;
	/** The code to type there (device sign-ins). */
	userCode?: string;
	/** The person pastes back a code or the address the browser landed on. */
	paste: boolean;
	intervalSeconds: number;
	expiresAt: string;
}

/** What an import kept and what it refused. */
export interface CredentialImport {
	created: Credential[];
	rejected: string[];
}
