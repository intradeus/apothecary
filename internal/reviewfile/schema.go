package reviewfile

// The JSON Schemas of the three documents, as the flow parser registers
// them for their built-in types and the agent reads them in its OUTPUT
// CONTRACT. The descriptions ARE the agent's documentation of the shape:
// what each field is, and which ones are never posted. What a schema cannot
// say (a path inside the repository, a thread the round loaded, a reply for
// every thread) the parsers above and the engine check.

// SchemaReviewThreads is the shape of a review_threads document.
const SchemaReviewThreads = `{
  "type": "object",
  "description": "a review of the merge request: a summary, notes that open threads on lines of the change, and replies in threads already on it",
  "properties": {
    "summary": {"type": "string", "description": "optional: the review's body, markdown — POSTED; on a re-review, what is new and what the author addressed"},
    "notes": {
      "type": "array",
      "description": "new threads to open, one per note — only for what no existing thread covers",
      "items": {
        "type": "object",
        "properties": {
          "path": {"type": "string", "minLength": 1, "description": "the file, relative to the repository root"},
          "line": {"type": "integer", "minimum": 1, "description": "the line in the NEW version of the file, as numbered now"},
          "kind": {"enum": ["blocking", "suggestion", "question", "nit"], "description": "what the note asks of the author; rendered as a leading [kind] tag when posted"},
          "body": {"type": "string", "minLength": 1, "description": "the note — POSTED on that line: what is wrong and what would fix it"},
          "rationale": {"type": "string", "minLength": 1, "description": "NEVER POSTED — for the person who decides whether to post the note: the intent you read in the change, why this matters"},
          "evidence": {"type": "string", "description": "NEVER POSTED — optional: what you checked (tests run, lines read, docs)"},
          "example": {"type": "string", "description": "NEVER POSTED — optional: a sketch of the fix"}
        },
        "required": ["path", "line", "kind", "body", "rationale"],
        "additionalProperties": false
      }
    },
    "replies": {
      "type": "array",
      "description": "replies in threads already on the merge request (ids exactly as the threads input gives them): your own earlier note is addressed or still open, or an answer to someone",
      "items": {
        "type": "object",
        "properties": {
          "thread": {"type": "string", "minLength": 1, "description": "the thread id, verbatim from the threads input"},
          "body": {"type": "string", "minLength": 1, "description": "the reply — POSTED in that thread"},
          "resolve": {"type": "boolean", "description": "optional: resolve the thread — only one apothecary opened, once the author addressed it"},
          "rationale": {"type": "string", "description": "NEVER POSTED — optional: why, for the person who decides whether to post it"}
        },
        "required": ["thread", "body"],
        "additionalProperties": false
      }
    }
  },
  "additionalProperties": false
}`

// SchemaReviewReplies is the shape of a review_replies document.
const SchemaReviewReplies = `{
  "type": "array",
  "description": "one entry per thread the round loaded: what was done about it, and the reply to post",
  "items": {
    "type": "object",
    "properties": {
      "thread": {"type": "string", "minLength": 1, "description": "the thread id, exactly as the comments input gives it"},
      "did": {"enum": ["fixed", "declined", "answered"], "description": "fixed: the code changed; declined: left as it is on purpose; answered: the thread asked something and the reply answers it"},
      "reply": {"type": "string", "minLength": 1, "description": "the reply — POSTED in the thread: concretely what changed and where, or why not"},
      "resolve": {"type": "boolean", "description": "optional: resolve the thread (never with declined — the reviewer closes a disagreement)"},
      "rationale": {"type": "string", "description": "NEVER POSTED — for the person who decides whether to post the reply: what exactly changed, or why the comment was not followed (required with declined)"},
      "evidence": {"type": "string", "description": "NEVER POSTED — optional: the commit, the test, the lines"}
    },
    "required": ["thread", "did", "reply"],
    "additionalProperties": false
  }
}`

// SchemaMRThreads is the shape of an mr_threads document.
const SchemaMRThreads = `{
  "type": "object",
  "description": "the review threads on the merge request as apothecary read them, and the notes a person declined to post earlier in this run",
  "properties": {
    "merge_request": {"type": "string", "description": "the merge request's page"},
    "threads": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string", "minLength": 1, "description": "the thread id a reply names, verbatim"},
          "path": {"type": "string", "description": "the file the thread is on; absent for a conversation comment"},
          "line": {"type": "integer", "description": "the line, as the host places it now"},
          "resolved": {"type": "boolean"},
          "ours": {"type": "boolean", "description": "apothecary opened this thread (one of its review notes)"},
          "kind": {"type": "string", "description": "the note's kind, when apothecary opened it"},
          "hunk": {"type": "string", "description": "the diff hunk the host shows with the thread"},
          "comments": {
            "type": "array",
            "description": "oldest first",
            "items": {
              "type": "object",
              "properties": {
                "id": {"type": "string"},
                "author": {"type": "string"},
                "at": {"type": "string", "description": "RFC 3339"},
                "ours": {"type": "boolean", "description": "apothecary wrote it"},
                "new": {"type": "boolean", "description": "written after apothecary's last word in the thread: what a round answers"},
                "did": {"type": "string", "description": "what an earlier reply of apothecary's said it did"},
                "body": {"type": "string"}
              },
              "required": ["id", "author", "body"],
              "additionalProperties": false
            }
          }
        },
        "required": ["id", "resolved", "ours", "comments"],
        "additionalProperties": false
      }
    },
    "dropped": {
      "type": "array",
      "description": "notes a person declined to post at a gate earlier in this run: never propose them again",
      "items": {
        "type": "object",
        "properties": {
          "path": {"type": "string"},
          "line": {"type": "integer"},
          "kind": {"type": "string"},
          "body": {"type": "string"},
          "rationale": {"type": "string"},
          "evidence": {"type": "string"},
          "example": {"type": "string"}
        },
        "required": ["path", "line", "kind", "body"],
        "additionalProperties": false
      }
    }
  },
  "required": ["threads"],
  "additionalProperties": false
}`
