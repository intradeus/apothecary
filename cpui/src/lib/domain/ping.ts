// A ping: a question waiting on a human — a flow's gate, or an agent's own
// ask_human.

/** 'gate' is every human_gate stage (approve / reject with feedback; what it
 *  asks lives in its question). The rest are raised by agents. */
export type PingType = 'gate' | 'clarification' | 'blocker' | 'permission';

export type PingStatus = 'pending' | 'answered' | 'withdrawn';

export interface Ping {
  id: string;
  runId: string;
  type: PingType;
  status: PingStatus;
  stageId?: string;
  question: string;
  context?: string;
  /** Attached workspace paths; the first is the primary reviewed document. */
  files: string[];
  /** A resolved URL payload (a gate's mr_url / workspace sentinel). */
  link?: string;
  /** The derived primary: the link, else the first file. */
  primaryRef?: string;
  /** The agent's closed answer set, when it asked a closed question. */
  options: string[];
  answer?: string;
  feedback?: string;
  answerAttachmentIds: string[];
  raisedAt: string;
  answeredAt?: string;
}

/** One of a ping's attached files: the declared ref joined with its
 *  raise-time snapshot. captured:false = the capture failed — shown as such,
 *  never silently substituted with live workspace content. */
export interface PingFile {
  path: string;
  captured: boolean;
  snapshotId?: string;
  mime?: string;
  size?: number;
}

export interface PingPayload {
  files: PingFile[];
  link?: string;
}
