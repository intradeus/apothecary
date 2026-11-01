// Integrations: configured instances of the kinds this build carries, and the
// notification channels.

/** Model auth is harness-owned, so there is no provider port. 'ticketsource'
 *  is a ticket tracker: a remote one tickets mirror, or the user's built-in
 *  internal one (kind 'internal'). Unlike the others it is not referenced by
 *  a project — a remote one is picked per import. */
export type Port = 'repository' | 'provisioner' | 'harness' | 'ticketsource';

export interface Integration {
	id: string;
	port: Port;
	kind: string;
	name: string;
	config: Record<string, unknown>;
	/** Masks only — secret values never round-trip. */
	secrets: Record<string, string>;
	createdAt: string;
	updatedAt: string;
}

export interface IntegrationDraft {
	port: Port;
	kind: string;
	name: string;
	config: Record<string, unknown>;
	secrets: Record<string, string>;
}

export interface KindField {
	name: string;
	label: string;
	placeholder?: string;
	default?: string;
	secret: boolean;
	required: boolean;
	/** Multi-line input (a `*_json` secret always is). */
	multiline?: boolean;
	/** One-line hint under the input; bare URLs are linkified by the form. */
	help?: string;
	/** "credentials": a pick of the owner's provider credentials this
	 *  harness takes (the value is their ids). */
	type?: 'credentials';
}

export interface KindDescriptor {
	port: Port;
	kind: string;
	label: string;
	/** Setup prose shown above the form (where to get the credential, etc.). */
	help?: string;
	fields: KindField[];
}

export interface ChannelEntry {
	id: string;
	kind: string;
	config: Record<string, unknown>;
}
