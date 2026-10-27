// Integrations: configured instances of the kinds this build carries, and the
// notification channels.

/** Model auth is harness-owned, so there is no provider port. 'ticketsource'
 *  is the read-only tracker tickets are imported from; unlike the others it is
 *  not referenced by a project — it is picked per import. */
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
