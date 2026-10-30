// Accounts (TODO 25): who is signed in, how they prove it, and their
// personal tokens. There is no organisation and no role beyond the instance
// admin, who manages accounts and sees nothing of anyone else's work.

export interface User {
	id: string;
	/** The short unique name: lowercase, what one may sign in with. */
	handle: string;
	email: string;
	name: string;
	/** Manages accounts. Grants no view into other users' work. */
	admin: boolean;
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
}

/** A personal API token: never its secret, which is shown once at creation. */
export interface Token {
	id: string;
	name: string;
	prefix: string;
	createdAt: string;
	lastUsedAt?: string;
	expiresAt?: string;
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
