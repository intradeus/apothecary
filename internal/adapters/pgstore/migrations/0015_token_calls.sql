-- What a personal token did: each write it made through the API and each
-- tool of the MCP face it called, refusals included. The act itself names
-- the token's person, as one made in a browser does; this names the token.
-- A token keeps its last 200 calls; deleting it deletes them.

CREATE TABLE api_token_calls (
    seq      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_id text NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
    at       timestamptz NOT NULL,
    method   text NOT NULL,
    route    text NOT NULL,
    path     text NOT NULL,
    tool     text NOT NULL DEFAULT '',
    status   integer NOT NULL
);
CREATE INDEX api_token_calls_token_idx ON api_token_calls (token_id, seq DESC);
