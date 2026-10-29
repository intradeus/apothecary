// The skill library (TODO 38): Agent Skills folders a person keeps, grouped
// into versioned skillsets a run installs. Frontend-owned shapes; the
// mappers translate the wire.

/** Where an imported skill or skillset came from, and what the last fetch
 *  found. Imports are public: a github.com or GitLab folder, or a direct
 *  archive link. */
export interface SkillSource {
	kind: 'github' | 'gitlab' | 'archive' | string;
	url: string;
	host?: string;
	owner?: string;
	repo?: string;
	ref?: string;
	path?: string;
	commit?: string;
	fetchedAt: string;
}

/** One file of a skill's manifest: its path, content hash, size and the
 *  permission bits it installs with (0o755 under scripts/, else 0o644). */
export interface SkillFileEntry {
	path: string;
	sha256: string;
	size: number;
	mode: number;
}

/** One file with its bytes, as the editor holds it. */
export interface SkillFileContent {
	path: string;
	bytes: Uint8Array;
	mode?: number;
}

export interface Skill {
	id: string;
	/** The folder name it installs as; equals SKILL.md's frontmatter name. */
	name: string;
	description: string;
	files: SkillFileEntry[];
	hash: string;
	source?: SkillSource;
	createdAt: string;
	updatedAt: string;
}

export interface SkillDetail {
	skill: Skill;
	contents: SkillFileContent[];
}

/** What a skill write carries: the whole folder. */
export interface SkillDraft {
	name: string;
	files: SkillFileContent[];
}

export interface Skillset {
	id: string;
	name: string;
	description: string;
	source?: SkillSource;
	/** The latest version's number (versions start at 1). */
	headSeq: number;
	/** Current members, in order. */
	skillIds: string[];
	createdAt: string;
	updatedAt: string;
}

export interface SkillsetDraft {
	name: string;
	description?: string;
	skillIds: string[];
}

export interface SkillsetVersionMember {
	skillId: string;
	name: string;
	description: string;
	hash: string;
	files: SkillFileEntry[];
}

/** An immutable snapshot of a skillset: v{seq}, its content hash, its
 *  members as they were. */
export interface SkillsetVersion {
	skillsetId: string;
	seq: number;
	hash: string;
	members: SkillsetVersionMember[];
	createdAt: string;
}

/** A run's request for a skillset: by id, at a version (undefined or 0 =
 *  the latest at the time the run starts). */
export interface SkillsetRef {
	id: string;
	seq?: number;
}

/** A skillset version a run installed, as pinned at its creation. */
export interface SkillsetPin {
	id: string;
	name: string;
	seq: number;
	hash: string;
}

/** What an import made: one skill, or a skillset and its skills. Warnings
 *  name what the folder held that could not become a skill. */
export interface SkillImportResult {
	skill?: Skill;
	skillset?: Skillset;
	skills: Skill[];
	warnings: string[];
}

export interface SkillsetRefresh {
	skillset: Skillset;
	warnings: string[];
}
