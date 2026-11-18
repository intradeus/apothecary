// Accounts (TODO 25): who is signed in, how they prove it, and their
// personal tokens. There is no organisation: an account's instance role says
// what it may do on the instance, and a workshop's owner shares it with
// others (project.ts: ProjectAccess).

/** An instance role: admin manages accounts and the instance, member owns
 *  workshops, guest works only in workshops shared with it. */
export type UserRole = 'admin' | 'member' | 'guest';

export interface User {
	id: string;
	/** The short unique name: lowercase, what one may sign in with. */
	handle: string;
	email: string;
	name: string;
	/** What the account may do on the instance. An admin manages accounts
	 *  and sees nothing of anyone else's work. */
	role: UserRole;
	disabled: boolean;
	lastSeenAt?: string;
	createdAt: string;
}

/** The signed-in user, with the kinds of method they can sign in by
 *  ('password' today; 'oauth', 'passkey' later). */
export interface Me {
	user: User;
	methods: string[];
}

/** What the UI asks before anything else. */
export interface AuthStatus {
	/** No account exists yet: the setup page is the only page. */
	setupRequired: boolean;
	/** Authentication methods this instance can verify. */
	methods: string[];
	/** The engine's build version ("v3.4.0", or "dev"). */
	version: string;
}

/** A personal API token: never its secret, which is shown once at creation. */
export interface Token {
	id: string;
	name: string;
	prefix: string;
	createdAt: string;
	lastUsedAt?: string;
	expiresAt?: string;
	/** An inactivity window, in seconds: the token signs out after that long
	 *  unused, each use sliding it. What the CLI's browser sign-in mints. */
	idleSeconds?: number;
	/** The actions the token is limited to; absent = full access. */
	actions?: string[];
	/** The workshops a limited token is limited to; absent = every one. */
	projectIds?: string[];
}

/** What a new token may do: everything its account may, or only the chosen
 *  actions, in the chosen workshops (none chosen = every workshop). */
export type TokenGrant = { full: true } | { full: false; actions: string[]; projectIds: string[] };

/** One action of the vocabulary a token can be limited to: `kind:verb`. */
export interface Permission {
	action: string;
	/** instance, account, project, run, ticket, … */
	kind: string;
	doc: string;
	/** Whether a token may be limited to it (account:manage never is). */
	grantable: boolean;
}

/** A terminal sign-in waiting for its approval in the browser
 *  (`apothecary login`): the code the terminal shows, and what the token it
 *  gets will be called. */
export interface CliLogin {
	/** As the terminal prints it: ABCD-EFGH. */
	userCode: string;
	name: string;
	expiresAt: string;
}

/** A public key registered for the engine's SSH door (TODO 46): what one logs
 *  into a run's workspace with. The private half never leaves one's machine. */
export interface SshKey {
	id: string;
	name: string;
	/** The algorithm, as the .pub line names it: ssh-ed25519, ssh-rsa, … */
	type: string;
	/** The authorized_keys line, without its comment. */
	publicKey: string;
	/** SHA256:… — what `ssh-keygen -lf` prints; the handle a person recognises. */
	fingerprint: string;
	createdAt: string;
	lastUsedAt?: string;
}

/** The deployment's capacity caps; `null` means no cap. */
export interface Limits {
	maxConcurrentRuns: number | null;
	maxEnvironments: number | null;
}
